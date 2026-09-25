package tickets

import (
	"context"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/gate"
)

// Jalon 41 : un agent par ticket (Claude Code ou modèle local), pilote
// automatique (plan et diff validés sans l'utilisateur) et relève des
// brouillons.

func claudeSet() (*AgentSet, *fakeAnalyst, *fakeDeveloper, *fakeReviewer) {
	a := &fakeAnalyst{plan: "## Plan de Claude"}
	d := &fakeDeveloper{summaries: []string{"fait par Claude"}}
	r := &fakeReviewer{results: []ReviewResult{{Approved: true, Summary: "RAS"}}}
	return &AgentSet{Analyst: a, Developer: d, Reviewer: r}, a, d, r
}

func TestManager_EachTicketUsesItsAgent(t *testing.T) {
	local := &fakeAnalyst{plan: "## Plan local"}
	m, s := newManager(local)
	set, claude, _, _ := claudeSet()
	m.Claude = set
	g := gate.New(1)
	var profiles []string
	g.Switch = func(ctx context.Context, p string) error { profiles = append(profiles, p); return nil }
	m.Gate = g
	ctx := context.Background()

	tc, err := m.CreateFor(ctx, "par Claude", "besoin", "", AgentClaude)
	if err != nil || tc.Agent != AgentClaude {
		t.Fatalf("CreateFor = %+v, %v", tc, err)
	}
	m.StartAnalysis(ctx, tc.ID)
	if got := waitTicket(t, s, tc.ID, PlanReady); got.Plan != "## Plan de Claude" {
		t.Errorf("plan = %q, want Claude's", got.Plan)
	}
	if len(local.Requests()) != 0 || len(claude.Requests()) != 1 {
		t.Errorf("analyses: local %d, claude %d", len(local.Requests()), len(claude.Requests()))
	}
	if len(profiles) != 0 {
		t.Errorf("a Claude ticket switched the local models to %v", profiles)
	}

	tl, _ := m.Create(ctx, "en local", "besoin", "")
	if tl.Agent != AgentLocal {
		t.Errorf("default agent = %q, want local", tl.Agent)
	}
	m.StartAnalysis(ctx, tl.ID)
	if got := waitTicket(t, s, tl.ID, PlanReady); got.Plan != "## Plan local" || len(profiles) != 1 {
		t.Errorf("local ticket: plan %q, profiles %v", got.Plan, profiles)
	}

	m.DefaultAgent = AgentClaude
	if tk, _ := m.Create(ctx, "défaut", "", ""); tk.Agent != AgentClaude {
		t.Errorf("DefaultAgent ignored: %q", tk.Agent)
	}
	if _, err := m.CreateFor(ctx, "x", "", "", "gpt"); err == nil {
		t.Error("unknown agent accepted")
	}
	m.Claude = nil
	if _, err := m.CreateFor(ctx, "x", "", "", AgentClaude); err == nil {
		t.Error("Claude ticket accepted while Claude is not configured")
	}
}

func TestManager_SetAgentOnlyWhenIdle(t *testing.T) {
	m, s := newManager(&fakeAnalyst{plan: "p", proceed: make(chan struct{})})
	set, _, _, _ := claudeSet()
	m.Claude = set
	ctx := context.Background()
	tk, _ := m.Create(ctx, "t", "", "")
	if err := m.SetAgent(ctx, tk.ID, AgentClaude); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, tk.ID); got.Agent != AgentClaude || !hasEvent(got, EventStatus, "Claude") {
		t.Errorf("agent = %q, events %+v", got.Agent, got.Events)
	}
	m.SetAgent(ctx, tk.ID, AgentLocal)
	m.StartAnalysis(ctx, tk.ID)
	if err := m.SetAgent(ctx, tk.ID, AgentClaude); err == nil {
		t.Error("agent changed while the agent is working")
	}
}

// Pilote automatique : plan validé, développé, relu, déployé — sans
// l'utilisateur, seulement si la vérification et la relecture passent.
func TestManager_AutopilotGoesAllTheWayToDeployment(t *testing.T) {
	m, s := newManager(&fakeAnalyst{})
	set, _, dev, _ := claudeSet()
	m.Claude, m.Autopilot = set, true
	m.Workspace, m.Verifier = &fakeWorkspace{diff: "+++ b/x_test.go\n+x"}, &fakeVerifier{results: []bool{true}}
	dep := &fakeDeployer{}
	m.Deployer, m.DeployPoll = dep, 10*time.Millisecond
	ctx := context.Background()

	tk, _ := m.CreateFor(ctx, "auto", "besoin", "", AgentClaude)
	if err := m.StartAnalysis(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(dep.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(dep.Calls()) != 1 || len(dev.Requests()) != 1 || dev.Requests()[0].Plan != "## Plan de Claude" {
		t.Fatalf("deploys %v, dev requests %+v", dep.Calls(), dev.Requests())
	}
	got, _, _ := s.Get(ctx, tk.ID)
	for _, want := range []string{"Plan validé automatiquement", "Déploiement automatique"} {
		if !hasEvent(got, EventStatus, want) {
			t.Errorf("thread lacks %q: %+v", want, got.Events)
		}
	}
}

func TestManager_AutopilotStopsAtHumanReviewWithoutApproval(t *testing.T) {
	for name, rev := range map[string]Reviewer{
		"rejected":    &fakeReviewer{results: []ReviewResult{{Approved: false, Summary: "bug"}}},
		"no reviewer": nil,
	} {
		t.Run(name, func(t *testing.T) {
			m, s := newManager(&fakeAnalyst{})
			set, _, _, _ := claudeSet()
			set.Reviewer = rev
			m.Claude, m.Autopilot, m.MaxReviewRounds = set, true, 1
			m.Workspace, m.Verifier = &fakeWorkspace{diff: "+++ b/x_test.go\n+x"}, &fakeVerifier{results: []bool{true}}
			dep := &fakeDeployer{}
			m.Deployer = dep
			ctx := context.Background()
			tk, _ := m.CreateFor(ctx, "auto", "besoin", "", AgentClaude)
			m.StartAnalysis(ctx, tk.ID)
			got := waitTicket(t, s, tk.ID, Review)
			time.Sleep(20 * time.Millisecond)
			if len(dep.Calls()) != 0 {
				t.Error("deployed without an approved review")
			}
			if !hasEvent(got, EventStatus, "à ta validation") {
				t.Errorf("thread does not say why it stopped: %+v", got.Events)
			}
		})
	}
}

func TestManager_WithoutAutopilotThePlanWaits(t *testing.T) {
	m, s := newManager(&fakeAnalyst{})
	set, _, dev, _ := claudeSet()
	m.Claude = set
	m.Workspace, m.Verifier = &fakeWorkspace{diff: "+x"}, &fakeVerifier{results: []bool{true}}
	tk, _ := m.CreateFor(context.Background(), "t", "", "", AgentClaude)
	m.StartAnalysis(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, PlanReady)
	time.Sleep(20 * time.Millisecond)
	if len(dev.Requests()) != 0 {
		t.Error("developed without the plan being approved")
	}
}

// Relève : le plus ancien brouillon, un seul ticket en cours à la fois.
func TestManager_PickUpStartsTheOldestDraftWhenIdle(t *testing.T) {
	analyst := &fakeAnalyst{plan: "p", proceed: make(chan struct{})}
	m, s := newManager(analyst)
	ctx := context.Background()
	first, _ := m.Create(ctx, "premier", "", "")
	time.Sleep(2 * time.Millisecond)
	m.Create(ctx, "second", "", "")

	id, err := m.PickUp(ctx)
	if err != nil || id != first.ID {
		t.Fatalf("PickUp = %q, %v, want the oldest draft %s", id, err, first.ID)
	}
	if got, _, _ := s.Get(ctx, first.ID); got.Status != Analyzing || !hasEvent(got, EventStatus, "relève") {
		t.Errorf("picked ticket = %s, events %+v", got.Status, got.Events)
	}
	if id, _ := m.PickUp(ctx); id != "" {
		t.Errorf("PickUp while a ticket is active = %q, want nothing", id)
	}
	close(analyst.proceed)
	waitTicket(t, s, first.ID, PlanReady)
	if id, _ := m.PickUp(ctx); id == "" || id == first.ID {
		t.Errorf("PickUp after = %q, want the second draft", id)
	}

	empty, _ := newManager(&fakeAnalyst{})
	if id, err := empty.PickUp(ctx); id != "" || err != nil {
		t.Errorf("PickUp without drafts = %q, %v", id, err)
	}
}
