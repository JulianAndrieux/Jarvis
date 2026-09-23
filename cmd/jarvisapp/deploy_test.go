package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/deploy"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

type fakeBase struct {
	head       string
	restoredTo string
	err        error
}

func (b *fakeBase) BaseHead(ctx context.Context) (string, error) { return b.head, nil }
func (b *fakeBase) RestoreBase(ctx context.Context, prev, msg string) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	b.restoredTo = prev
	return "rev9999999", nil
}

func deployingTicket(t *testing.T) (*tickets.Manager, *tickets.FakeStore, string) {
	t.Helper()
	store := tickets.NewFakeStore()
	store.Create(context.Background(), tickets.Ticket{ID: "t1", Title: "Commentaires", Status: tickets.Deploying, CreatedAt: time.Now()})
	return &tickets.Manager{Store: store}, store, filepath.Join(t.TempDir(), "deploy.json")
}

func lastEvent(tk tickets.Ticket) string {
	if len(tk.Events) == 0 {
		return ""
	}
	e := tk.Events[len(tk.Events)-1]
	return e.Text + "\n" + e.Detail
}

func TestSettleDeployment_NoMarker(t *testing.T) {
	tm, _, marker := deployingTicket(t)
	if msg, err := settleDeployment(context.Background(), marker, tm, &fakeBase{}); msg != "" || err != nil {
		t.Errorf("settleDeployment() = %q, %v", msg, err)
	}
}

// La nouvelle version a démarré : elle confirme son propre déploiement.
func TestSettleDeployment_Confirms(t *testing.T) {
	tm, store, marker := deployingTicket(t)
	deploy.WriteMarker(marker, deploy.Marker{TicketID: "t1", Commit: "abc1234ffff", PrevCommit: "0000000"})
	msg, err := settleDeployment(context.Background(), marker, tm, &fakeBase{head: "abc1234ffff"})
	if err != nil || !strings.Contains(msg, "t1") {
		t.Fatalf("settleDeployment() = %q, %v", msg, err)
	}
	tk, _, _ := store.Get(context.Background(), "t1")
	if tk.Status != tickets.Deployed || !strings.Contains(lastEvent(tk), "abc1234") {
		t.Errorf("ticket %s, last event %q", tk.Status, lastEvent(tk))
	}
	if _, ok, _ := deploy.ReadMarker(marker); ok {
		t.Error("marker not removed")
	}
}

// L'ancienne version, rétablie par le lanceur : main revient à l'état
// d'avant, le ticket repart en revue avec la raison.
func TestSettleDeployment_RolledBackRestoresMain(t *testing.T) {
	tm, store, marker := deployingTicket(t)
	deploy.WriteMarker(marker, deploy.Marker{TicketID: "t1", Commit: "abc1234ffff", PrevCommit: "0000000", RolledBack: true, Reason: "jarvisapp s'est arrêté (exit status 2)\npanic: nil map"})
	base := &fakeBase{head: "abc1234ffff"}
	if _, err := settleDeployment(context.Background(), marker, tm, base); err != nil {
		t.Fatal(err)
	}
	if base.restoredTo != "0000000" {
		t.Errorf("main restored to %q", base.restoredTo)
	}
	tk, _, _ := store.Get(context.Background(), "t1")
	if tk.Status != tickets.Review || !strings.Contains(lastEvent(tk), "panic: nil map") || !strings.Contains(lastEvent(tk), "rev9999") {
		t.Errorf("ticket %s, last event %q", tk.Status, lastEvent(tk))
	}
}

// main a bougé depuis (commit à la main) : on ne le touche pas, on le dit.
func TestSettleDeployment_RolledBackButMainMovedIsLeftAlone(t *testing.T) {
	tm, store, marker := deployingTicket(t)
	deploy.WriteMarker(marker, deploy.Marker{TicketID: "t1", Commit: "abc1234ffff", PrevCommit: "0000000", RolledBack: true, Reason: "arrêt"})
	base := &fakeBase{head: "fffffff"}
	settleDeployment(context.Background(), marker, tm, base)
	if base.restoredTo != "" {
		t.Error("main restored although it moved since the deployment")
	}
	tk, _, _ := store.Get(context.Background(), "t1")
	if tk.Status != tickets.Review || !strings.Contains(lastEvent(tk), "main a bougé") {
		t.Errorf("ticket %s, last event %q", tk.Status, lastEvent(tk))
	}
}

func TestSettleDeployment_RestoreErrorIsReported(t *testing.T) {
	tm, store, marker := deployingTicket(t)
	deploy.WriteMarker(marker, deploy.Marker{TicketID: "t1", Commit: "abc", PrevCommit: "000", RolledBack: true, Reason: "arrêt"})
	settleDeployment(context.Background(), marker, tm, &fakeBase{head: "abc", err: errors.New("travail non commité")})
	tk, _, _ := store.Get(context.Background(), "t1")
	if !strings.Contains(lastEvent(tk), "travail non commité") {
		t.Errorf("last event %q", lastEvent(tk))
	}
}
