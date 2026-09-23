package tickets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/gate"
)

type fakeAnalyst struct {
	mu      sync.Mutex
	plan    string
	err     error
	steps   []AgentStep
	got     []AnalysisRequest
	proceed chan struct{}
}

func (a *fakeAnalyst) Analyze(ctx context.Context, req AnalysisRequest, onStep func(AgentStep)) (string, error) {
	a.mu.Lock()
	a.got = append(a.got, req)
	a.mu.Unlock()
	if a.proceed != nil {
		<-a.proceed
	}
	for _, s := range a.steps {
		onStep(s)
	}
	return a.plan, a.err
}

func (a *fakeAnalyst) Requests() []AnalysisRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AnalysisRequest(nil), a.got...)
}

func waitTicket(t *testing.T, s Store, id string, want Status) Ticket {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _, _ := s.Get(context.Background(), id)
		if tk.Status == want {
			return tk
		}
		time.Sleep(2 * time.Millisecond)
	}
	tk, _, _ := s.Get(context.Background(), id)
	t.Fatalf("ticket status = %s, want %s", tk.Status, want)
	return tk
}

func newManager(a Analyst) (*Manager, *FakeStore) {
	s := NewFakeStore()
	return &Manager{Store: s, Analyst: a}, s
}

func TestManager_CreateRequiresTitleAndStartsAsDraft(t *testing.T) {
	m, _ := newManager(&fakeAnalyst{})
	if _, err := m.Create(context.Background(), "  ", "besoin", ""); err == nil {
		t.Error("Create with an empty title: error = nil")
	}
	tk, err := m.Create(context.Background(), "Filtre par date", "Filtrer par date d'import", "?since= fonctionne")
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID == "" || tk.Status != Draft || tk.Need != "Filtrer par date d'import" {
		t.Errorf("created = %+v", tk)
	}
}

func TestManager_AnalysisRecordsStepsAndPlan(t *testing.T) {
	a := &fakeAnalyst{plan: "## Étapes\n1. x", steps: []AgentStep{{Summary: "Lit store.go", Detail: "type ListQuery"}}}
	m, s := newManager(a)
	tk, _ := m.Create(context.Background(), "T", "besoin", "critère")

	if err := m.StartAnalysis(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	done := waitTicket(t, s, tk.ID, PlanReady)
	if done.Plan != "## Étapes\n1. x" {
		t.Errorf("plan = %q", done.Plan)
	}
	var kinds []EventKind
	for _, e := range done.Events {
		kinds = append(kinds, e.Kind)
	}
	joined := strings.Join(func() []string {
		out := make([]string, len(kinds))
		for i, k := range kinds {
			out[i] = string(k)
		}
		return out
	}(), ",")
	if !strings.Contains(joined, "status,step,plan") {
		t.Errorf("event kinds = %s, want the analysis start, the agent step, then the plan", joined)
	}
	if req := a.Requests()[0]; req.Title != "T" || req.Acceptance != "critère" {
		t.Errorf("analyst got %+v", req)
	}
}

func TestManager_AnalysisErrorMarksFailedWithReason(t *testing.T) {
	m, s := newManager(&fakeAnalyst{err: errors.New("llm down")})
	tk, _ := m.Create(context.Background(), "T", "b", "")
	m.StartAnalysis(context.Background(), tk.ID)
	failed := waitTicket(t, s, tk.ID, Failed)
	last := failed.Events[len(failed.Events)-1]
	if last.Kind != EventError || !strings.Contains(last.Text, "llm down") {
		t.Errorf("last event = %+v", last)
	}
	// Un échec se relance.
	if err := m.StartAnalysis(context.Background(), tk.ID); err != nil {
		t.Errorf("retry after failure: %v", err)
	}
}

// Révision : l'agent reçoit son plan précédent et les commentaires de
// l'utilisateur écrits depuis.
func TestManager_RevisionSendsPreviousPlanAndFeedback(t *testing.T) {
	a := &fakeAnalyst{plan: "plan v1"}
	m, s := newManager(a)
	tk, _ := m.Create(context.Background(), "T", "b", "")
	m.StartAnalysis(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, PlanReady)

	a.plan = "plan v2"
	m.Comment(context.Background(), tk.ID, "ajoute aussi un filtre until")
	if err := m.RequestRevision(context.Background(), tk.ID, "et garde la compatibilité"); err != nil {
		t.Fatal(err)
	}
	revised := waitTicket(t, s, tk.ID, PlanReady)
	if revised.Plan != "plan v2" {
		t.Errorf("plan = %q", revised.Plan)
	}
	req := a.Requests()[1]
	if req.PreviousPlan != "plan v1" || !strings.Contains(req.Feedback, "filtre until") || !strings.Contains(req.Feedback, "compatibilité") {
		t.Errorf("revision request = %+v", req)
	}
}

func TestManager_TransitionsAreEnforced(t *testing.T) {
	m, s := newManager(&fakeAnalyst{plan: "p"})
	tk, _ := m.Create(context.Background(), "T", "b", "")
	if err := m.ApprovePlan(context.Background(), tk.ID); err == nil {
		t.Error("approving a draft (no plan yet): error = nil")
	}
	m.StartAnalysis(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, PlanReady)
	if err := m.ApprovePlan(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.StartAnalysis(context.Background(), tk.ID); err == nil {
		t.Error("re-analysing an approved plan: error = nil")
	}
	if err := m.Cancel(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(context.Background(), tk.ID); got.Status != Cancelled {
		t.Errorf("status = %s", got.Status)
	}
}

// L'analyse attend son tour derrière la file partagée avec les documents.
func TestManager_AnalysisWaitsForSharedGate(t *testing.T) {
	g := gate.New(1)
	release := g.Acquire() // un document est en traitement
	a := &fakeAnalyst{plan: "p"}
	m, s := newManager(a)
	m.Gate = g
	tk, _ := m.Create(context.Background(), "T", "b", "")
	m.StartAnalysis(context.Background(), tk.ID)
	time.Sleep(30 * time.Millisecond)
	if len(a.Requests()) != 0 {
		t.Error("the agent ran while the gate was held")
	}
	release()
	waitTicket(t, s, tk.ID, PlanReady)
}

func TestManager_RecoverOrphanedMarksInterruptedAnalysesFailed(t *testing.T) {
	m, s := newManager(&fakeAnalyst{})
	s.Create(context.Background(), Ticket{ID: "x", Title: "T", Status: Analyzing, CreatedAt: time.Now()})
	n, err := m.RecoverOrphaned(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("RecoverOrphaned = %d, %v", n, err)
	}
	got, _, _ := s.Get(context.Background(), "x")
	if got.Status != Failed || !strings.Contains(got.Events[len(got.Events)-1].Text, "redémarrage") {
		t.Errorf("ticket = %+v", got)
	}
}
