package tickets

import (
	"context"
	"errors"
	"testing"
)

type fakePusher struct {
	err    error
	pushes int
}

func (p *fakePusher) Push(ctx context.Context) (string, error) {
	p.pushes++
	return "abc1234ffff", p.err
}

func deployedTicket(t *testing.T) (*Manager, *FakeStore, Ticket) {
	t.Helper()
	m, s, _, tk := reviewTicket(t, &fakeDeployer{})
	m.AcceptChanges(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Deploying)
	if err := m.ConfirmDeployment(context.Background(), tk.ID, "abc1234ffff"); err != nil {
		t.Fatal(err)
	}
	return m, s, tk
}

func TestManager_PushDeployedTicket(t *testing.T) {
	m, s, tk := deployedTicket(t)
	p := &fakePusher{}
	m.Pusher = p
	if err := m.Push(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	if got.Pushed != "abc1234ffff" || !hasEvent(got, EventStatus, "Poussé vers GitHub") {
		t.Errorf("pushed %q, events %+v", got.Pushed, got.Events)
	}
}

// Un refus (GitHub a avancé, pas de réseau) s'affiche dans le fil ; le
// ticket reste déployé, on peut réessayer.
func TestManager_PushFailureIsReported(t *testing.T) {
	m, s, tk := deployedTicket(t)
	m.Pusher = &fakePusher{err: errors.New("GitHub a des commits que main n'a pas")}
	if err := m.Push(context.Background(), tk.ID); err != nil {
		t.Fatalf("Push() = %v, want the failure recorded in the thread", err)
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	if got.Status != Deployed || got.Pushed != "" || !hasEvent(got, EventError, "GitHub a des commits") {
		t.Errorf("status %s pushed %q events %+v", got.Status, got.Pushed, got.Events)
	}
}

func TestManager_PushOnlyDeployedTickets(t *testing.T) {
	m, _, _, tk := reviewTicket(t, &fakeDeployer{})
	p := &fakePusher{}
	m.Pusher = p
	if err := m.Push(context.Background(), tk.ID); err == nil || p.pushes != 0 {
		t.Errorf("push of a ticket in review: err=%v pushes=%d", err, p.pushes)
	}
	m.Pusher = nil
	if err := m.Push(context.Background(), tk.ID); err == nil {
		t.Error("push without a pusher must fail")
	}
}
