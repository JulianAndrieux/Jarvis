package tickets

import (
	"context"
	"errors"
	"testing"
)

// Instance passive (rôle « cloud ») : on y crée et lit les tickets, mais
// aucun travail d'agent n'y démarre — c'est l'instance du Mac qui les
// prend.

func TestManager_Passive_CreatesForClaudeWithoutLocalClaude(t *testing.T) {
	m, _ := newManager(&fakeAnalyst{})
	m.Passive = true
	m.DefaultAgent = AgentClaude
	tk, err := m.Create(context.Background(), "T", "besoin", "")
	if err != nil {
		t.Fatalf("Create on a passive instance: %v", err)
	}
	if tk.Status != Draft || tk.Agent != AgentClaude {
		t.Errorf("created = %+v, want a draft for Claude", tk)
	}
	if err := m.SetAgent(context.Background(), tk.ID, AgentLocal); err != nil {
		t.Errorf("SetAgent: %v", err)
	}
	if err := m.Comment(context.Background(), tk.ID, "précision"); err != nil {
		t.Errorf("Comment: %v", err)
	}
}

func TestManager_Passive_RefusesEveryWorkAction(t *testing.T) {
	a := &fakeAnalyst{plan: "plan"}
	m, s := newManager(a)
	ctx := context.Background()
	tk, _ := m.Create(ctx, "T", "besoin", "")
	m.Passive = true

	actions := map[string]func() error{
		"StartAnalysis":    func() error { return m.StartAnalysis(ctx, tk.ID) },
		"RequestRevision":  func() error { return m.RequestRevision(ctx, tk.ID, "x") },
		"ApprovePlan":      func() error { return m.ApprovePlan(ctx, tk.ID) },
		"StartDevelopment": func() error { return m.StartDevelopment(ctx, tk.ID, "") },
		"RequestChanges":   func() error { return m.RequestChanges(ctx, tk.ID, "x") },
		"AcceptChanges":    func() error { return m.AcceptChanges(ctx, tk.ID) },
		"StartDeployment":  func() error { return m.StartDeployment(ctx, tk.ID) },
		"Push":             func() error { return m.Push(ctx, tk.ID) },
	}
	for name, act := range actions {
		if err := act(); !errors.Is(err, ErrPassive) {
			t.Errorf("%s on a passive instance: err = %v, want ErrPassive", name, err)
		}
	}
	got, _, _ := s.Get(ctx, tk.ID)
	if got.Status != Draft {
		t.Errorf("status = %s, want the draft untouched", got.Status)
	}
	if len(a.Requests()) != 0 {
		t.Errorf("the analyst was called %d time(s) on a passive instance", len(a.Requests()))
	}
}

func TestManager_Passive_NeverPicksUpNorRecovers(t *testing.T) {
	m, s := newManager(&fakeAnalyst{plan: "plan"})
	ctx := context.Background()
	draft, _ := m.Create(ctx, "T", "besoin", "")
	// Un ticket que l'instance du Mac est en train d'analyser.
	busy, _ := m.Create(ctx, "U", "besoin", "")
	b, _, _ := s.Get(ctx, busy.ID)
	b.Status = Analyzing
	if err := s.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	m.Passive = true

	if n, err := m.RecoverOrphaned(ctx); err != nil || n != 0 {
		t.Errorf("RecoverOrphaned = %d, %v ; want 0, nil", n, err)
	}
	if got, _, _ := s.Get(ctx, busy.ID); got.Status != Analyzing {
		t.Errorf("ticket worked by another instance = %s, want it left in analysis", got.Status)
	}
	b.Status = Cancelled
	_ = s.Update(ctx, b)
	if id, err := m.PickUp(ctx); err != nil || id != "" {
		t.Errorf("PickUp = %q, %v ; want nothing picked", id, err)
	}
	if got, _, _ := s.Get(ctx, draft.ID); got.Status != Draft {
		t.Errorf("draft = %s, want it left for the worker instance", got.Status)
	}
}

func TestManager_Passive_CancelsOnlyWhatNoAgentTouched(t *testing.T) {
	m, s := newManager(&fakeAnalyst{})
	ctx := context.Background()
	draft, _ := m.Create(ctx, "T", "besoin", "")
	busy, _ := m.Create(ctx, "U", "besoin", "")
	b, _, _ := s.Get(ctx, busy.ID)
	b.Status = Developing
	_ = s.Update(ctx, b)
	m.Passive = true

	if err := m.Cancel(ctx, draft.ID); err != nil {
		t.Errorf("Cancel a draft: %v", err)
	}
	if err := m.Cancel(ctx, busy.ID); !errors.Is(err, ErrPassive) {
		t.Errorf("Cancel a ticket in development: err = %v, want ErrPassive", err)
	}
}
