package tickets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Jalon 30 : accepter le diff le déploie ---

type fakeDeployer struct {
	mu    sync.Mutex
	err   error
	calls []string
}

func (d *fakeDeployer) Deploy(ctx context.Context, id string, onStep func(text, detail string)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, id)
	onStep("Vérification complète réussie", "ok")
	return d.err
}

func (d *fakeDeployer) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func reviewTicket(t *testing.T, dep Deployer) (*Manager, *FakeStore, *fakeWorkspace, Ticket) {
	t.Helper()
	ws := &fakeWorkspace{diff: "+x"}
	m, s, tk := approvedTicket(t, &fakeDeveloper{summaries: []string{"fait"}}, ws, &fakeVerifier{results: []bool{true}})
	m.Deployer = dep
	m.DeployPoll = 10 * time.Millisecond
	m.ApprovePlan(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Review)
	return m, s, ws, tk
}

func hasEvent(tk Ticket, kind EventKind, substr string) bool {
	for _, e := range tk.Events {
		if e.Kind == kind && strings.Contains(e.Text+"\n"+e.Detail, substr) {
			return true
		}
	}
	return false
}

func TestManager_AcceptDeploysAndRecordsSteps(t *testing.T) {
	dep := &fakeDeployer{}
	m, s, _, tk := reviewTicket(t, dep)
	if err := m.AcceptChanges(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(dep.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	// Le vrai déployeur remplace le processus ; c'est la nouvelle version
	// qui confirme. Ici le ticket attend cette confirmation.
	if got.Status != Deploying || !hasEvent(got, EventStep, "Vérification complète réussie") {
		t.Errorf("status %s, events %+v", got.Status, got.Events)
	}
	if !got.Status.Active() {
		t.Error("a deploying ticket is active (the page polls)")
	}
}

func TestManager_DeployFailureReturnsToReview(t *testing.T) {
	dep := &fakeDeployer{err: errors.New("conflit avec main dans a.go")}
	m, s, _, tk := reviewTicket(t, dep)
	m.AcceptChanges(context.Background(), tk.ID)
	got := waitTicket(t, s, tk.ID, Review)
	if !hasEvent(got, EventError, "conflit avec main dans a.go") {
		t.Errorf("events = %+v", got.Events)
	}
}

// Le redémarrage couperait un document en cours de traitement ou un autre
// ticket : le déploiement attend, et le dit.
func TestManager_DeployWaitsUntilIdle(t *testing.T) {
	dep := &fakeDeployer{}
	m, s, _, tk := reviewTicket(t, dep)
	var mu sync.Mutex
	busy := "1 document en cours de traitement"
	m.Busy = func(ctx context.Context) string {
		mu.Lock()
		defer mu.Unlock()
		return busy
	}
	m.AcceptChanges(context.Background(), tk.ID)
	time.Sleep(80 * time.Millisecond)
	if len(dep.Calls()) != 0 {
		t.Fatal("deployed while busy")
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	if !hasEvent(got, EventStatus, "1 document en cours de traitement") {
		t.Errorf("waiting not reported: %+v", got.Events)
	}
	mu.Lock()
	busy = ""
	mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for len(dep.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(dep.Calls()) != 1 {
		t.Error("not deployed once idle")
	}
}

// Au démarrage de la nouvelle version : confirmation, copie supprimée.
func TestManager_ConfirmDeployment(t *testing.T) {
	m, s, ws, tk := reviewTicket(t, &fakeDeployer{})
	m.AcceptChanges(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Deploying)
	if err := m.ConfirmDeployment(context.Background(), tk.ID, "abc1234def"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	if got.Status != Deployed || !hasEvent(got, EventStatus, "abc1234") {
		t.Errorf("status %s, events %+v", got.Status, got.Events)
	}
	if len(ws.discarded) != 1 || ws.discarded[0] != tk.ID {
		t.Errorf("workspace not discarded: %v", ws.discarded)
	}
}

func TestManager_DeploymentRolledBack(t *testing.T) {
	m, s, _, tk := reviewTicket(t, &fakeDeployer{})
	m.AcceptChanges(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Deploying)
	if err := m.DeploymentRolledBack(context.Background(), tk.ID, "arrêt au démarrage (exit 2)"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	if got.Status != Review || !hasEvent(got, EventError, "exit 2") {
		t.Errorf("status %s, events %+v", got.Status, got.Events)
	}
}

// Sans déployeur configuré, accepter reste une simple validation.
func TestManager_AcceptWithoutDeployerJustAccepts(t *testing.T) {
	m, s, _, tk := reviewTicket(t, nil)
	m.Deployer = nil
	m.AcceptChanges(context.Background(), tk.ID)
	if got, _, _ := s.Get(context.Background(), tk.ID); got.Status != Accepted {
		t.Errorf("status = %s", got.Status)
	}
}

func TestManager_RecoverOrphanedDeployment(t *testing.T) {
	m, s, _, tk := reviewTicket(t, &fakeDeployer{})
	m.AcceptChanges(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Deploying)
	if n, err := m.RecoverOrphaned(context.Background()); err != nil || n != 1 {
		t.Fatalf("RecoverOrphaned() = %d, %v", n, err)
	}
	got, _, _ := s.Get(context.Background(), tk.ID)
	if got.Status != Review || !hasEvent(got, EventError, "Déploiement interrompu") {
		t.Errorf("status %s, events %+v", got.Status, got.Events)
	}
}

func TestCanTransition_DeploymentPhase(t *testing.T) {
	for _, c := range []struct{ from, to Status }{{Review, Deploying}, {Accepted, Deploying}, {Deploying, Deployed}, {Deploying, Review}} {
		if !CanTransition(c.from, c.to) {
			t.Errorf("%s -> %s refused", c.from, c.to)
		}
	}
	for _, c := range []struct{ from, to Status }{{Deployed, Developing}, {Deployed, Cancelled}, {Deploying, Cancelled}, {PlanApproved, Deploying}} {
		if CanTransition(c.from, c.to) {
			t.Errorf("%s -> %s allowed", c.from, c.to)
		}
	}
}

// Un ticket accepté avant le jalon 30 (ou sans déployeur) se déploie
// ensuite à la demande.
func TestManager_StartDeploymentFromAccepted(t *testing.T) {
	m, s, _, tk := reviewTicket(t, nil)
	m.AcceptChanges(context.Background(), tk.ID)
	if err := m.StartDeployment(context.Background(), tk.ID); err == nil {
		t.Error("StartDeployment without a deployer must fail")
	}
	dep := &fakeDeployer{}
	m.Deployer = dep
	if err := m.StartDeployment(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	waitTicket(t, s, tk.ID, Deploying)
	deadline := time.Now().Add(2 * time.Second)
	for len(dep.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(dep.Calls()) != 1 {
		t.Error("not deployed")
	}
}
