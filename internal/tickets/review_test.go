package tickets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/gate"
)

// Jalon 36 : un relecteur (agent) relit le diff vérifié avant la revue
// humaine. Vu en réel (Devstral, ticket d'exemple) : deux diffs qui
// compilaient et passaient les tests, mais faux sur le fond.

type fakeReviewer struct {
	mu      sync.Mutex
	results []ReviewResult
	err     error
	got     []ReviewRequest
}

func (r *fakeReviewer) Review(ctx context.Context, req ReviewRequest, onStep func(AgentStep)) (ReviewResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, req)
	if r.err != nil {
		return ReviewResult{}, r.err
	}
	res := r.results[0]
	if len(r.results) > 1 {
		r.results = r.results[1:]
	}
	return res, nil
}

func (r *fakeReviewer) Requests() []ReviewRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReviewRequest(nil), r.got...)
}

var outsideForm = ReviewResult{Summary: "Les dates ne sont plus envoyées.", Issues: []ReviewIssue{
	{File: "cmd/jarvisapp/templates/documents.templ", Line: 119, Severity: "bloquant", Message: "Les champs de date sont hors du <form> : remets-les dedans."},
}}

func reviewedTicket(t *testing.T, rev Reviewer, dev *fakeDeveloper) (*Manager, *FakeStore, Ticket) {
	t.Helper()
	m, s, tk := approvedTicket(t, dev, &fakeWorkspace{diff: "+++ b/page.templ\n+<div>"}, &fakeVerifier{results: []bool{true}})
	m.Reviewer = rev
	return m, s, tk
}

func TestReview_ApprovedGoesToHumanReview(t *testing.T) {
	rev := &fakeReviewer{results: []ReviewResult{{Approved: true, Summary: "Répond au ticket."}}}
	dev := &fakeDeveloper{summaries: []string{"fait"}}
	m, s, tk := reviewedTicket(t, rev, dev)
	m.ApprovePlan(context.Background(), tk.ID)
	got := waitTicket(t, s, tk.ID, Review)
	if got.Review == nil || !got.Review.Approved || got.Review.Rounds != 1 || len(dev.Requests()) != 1 {
		t.Errorf("review = %+v, dev calls = %d", got.Review, len(dev.Requests()))
	}
	if req := rev.Requests()[0]; req.Diff != "+++ b/page.templ\n+<div>" || req.Need != "besoin" || req.Dir == "" {
		t.Errorf("review request = %+v", req)
	}
}

// Remarques du relecteur : le développeur est relancé avec elles, sans
// consommer ses tentatives de vérification.
func TestReview_ChangesAreSentBackToTheDeveloper(t *testing.T) {
	rev := &fakeReviewer{results: []ReviewResult{outsideForm, {Approved: true, Summary: "Corrigé."}}}
	dev := &fakeDeveloper{summaries: []string{"essai 1", "essai 2"}}
	m, s, tk := reviewedTicket(t, rev, dev)
	m.MaxAttempts = 1
	m.ApprovePlan(context.Background(), tk.ID)
	got := waitTicket(t, s, tk.ID, Review)
	reqs := dev.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1].Feedback, "hors du <form>") || !strings.Contains(reqs[1].Feedback, "documents.templ:119") {
		t.Fatalf("dev requests = %+v, want a second attempt with the review", reqs)
	}
	if got.Review == nil || !got.Review.Approved || got.Review.Rounds != 2 {
		t.Errorf("review = %+v", got.Review)
	}
}

// Relecteur jamais satisfait : après MaxReviewRounds allers-retours, le
// diff va quand même à la revue humaine, avec les remarques restantes.
func TestReview_UnresolvedGoesToHumanWithRemarks(t *testing.T) {
	rev := &fakeReviewer{results: []ReviewResult{outsideForm}}
	dev := &fakeDeveloper{summaries: []string{"x"}}
	m, s, tk := reviewedTicket(t, rev, dev)
	m.MaxReviewRounds = 2
	m.ApprovePlan(context.Background(), tk.ID)
	got := waitTicket(t, s, tk.ID, Review)
	if len(dev.Requests()) != 3 || got.Review == nil || got.Review.Approved || len(got.Review.Issues) != 1 {
		t.Errorf("dev calls = %d, review = %+v", len(dev.Requests()), got.Review)
	}
	last := got.Events[len(got.Events)-1]
	if !strings.Contains(got.Events[len(got.Events)-2].Text+last.Text, "à ta décision") {
		t.Errorf("events end = %+v", got.Events[len(got.Events)-2:])
	}
}

// Relecture impossible (modèle en panne) : le diff va à la revue humaine,
// avec la raison — jamais bloqué par le relecteur.
func TestReview_ErrorDoesNotBlock(t *testing.T) {
	rev := &fakeReviewer{err: errors.New("modèle muet")}
	m, s, tk := reviewedTicket(t, rev, &fakeDeveloper{summaries: []string{"x"}})
	m.ApprovePlan(context.Background(), tk.ID)
	got := waitTicket(t, s, tk.ID, Review)
	if got.Review == nil || got.Review.Approved || !strings.Contains(got.Review.Summary, "modèle muet") {
		t.Errorf("review = %+v", got.Review)
	}
}

func TestFormatReview(t *testing.T) {
	got := FormatReview(outsideForm)
	for _, want := range []string{"Les dates ne sont plus envoyées.", "[bloquant] cmd/jarvisapp/templates/documents.templ:119", "hors du <form>"} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatReview lacks %q:\n%s", want, got)
		}
	}
}

// Vu en réel : le relecteur recevait aussi le diff des fichiers générés
// (_templ.go, des centaines de lignes de code machine) — le vrai
// changement était noyé, puis coupé. Seuls les fichiers écrits à la main
// lui sont montrés.
func TestReview_DiffWithoutGeneratedFiles(t *testing.T) {
	diff := "diff --git a/p.templ b/p.templ\n--- a/p.templ\n+++ b/p.templ\n+<div>\ndiff --git a/p_templ.go b/p_templ.go\n--- a/p_templ.go\n+++ b/p_templ.go\n+templ_7745c5c3_Err = x\ndiff --git a/q.go b/q.go\n+++ b/q.go\n+x := 1\n"
	got := withoutGenerated(diff)
	if strings.Contains(got, "templ_7745") || !strings.Contains(got, "+<div>") || !strings.Contains(got, "+x := 1") {
		t.Errorf("withoutGenerated =\n%s", got)
	}
}

// Jalon 37 : l'analyse et le développement chargent le profil « code » ;
// une bascule impossible fait échouer le ticket avec la raison.
func TestManager_SwitchesToCodeModels(t *testing.T) {
	m, s := newManager(&fakeAnalyst{plan: "## Étapes\n1. x"})
	g := gate.New(1)
	var profiles []string
	g.Switch = func(ctx context.Context, p string) error { profiles = append(profiles, p); return nil }
	m.Gate = g
	tk, _ := m.Create(context.Background(), "t", "besoin", "")
	m.StartAnalysis(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, PlanReady)
	if len(profiles) != 1 || profiles[0] != gate.Code {
		t.Errorf("profiles = %v", profiles)
	}
	g.Switch = func(ctx context.Context, p string) error { return errors.New("Devstral jamais prêt") }
	tk2, _ := m.Create(context.Background(), "t2", "besoin", "")
	m.StartAnalysis(context.Background(), tk2.ID)
	failed := waitTicket(t, s, tk2.ID, Failed)
	if last := failed.Events[len(failed.Events)-1]; !strings.Contains(last.Text, "Devstral jamais prêt") {
		t.Errorf("last event = %+v", last)
	}
}

// Jalon 38 : relecture de code puis relecture visuelle, un seul verdict.
func TestMultiReviewer_MergesVerdicts(t *testing.T) {
	code := &fakeReviewer{results: []ReviewResult{{Approved: true, Summary: "Code correct."}}}
	visual := &fakeReviewer{results: []ReviewResult{{Summary: "À droite au lieu d'en dessous.",
		Issues:   []ReviewIssue{{File: "capture /documents", Severity: "important", Message: "sous la barre"}},
		Captures: []ReviewCapture{{Page: "/documents", Before: []byte("a"), After: []byte("b")}}}}}
	res, err := MultiReviewer{code, visual}.Review(context.Background(), ReviewRequest{}, nil)
	if err != nil || res.Approved || len(res.Issues) != 1 || len(res.Captures) != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !strings.Contains(res.Summary, "Code correct.") || !strings.Contains(res.Summary, "À droite") {
		t.Errorf("summary = %q", res.Summary)
	}
	ok := &fakeReviewer{results: []ReviewResult{{Approved: true, Summary: "ok"}}}
	if res, _ := (MultiReviewer{ok, ok}).Review(context.Background(), ReviewRequest{}, nil); !res.Approved {
		t.Error("both approve: not approved")
	}
}

// Une relecture impossible (Chrome absent, modèle en panne) est signalée,
// sans bloquer ni renvoyer le développeur pour rien.
func TestMultiReviewer_FailingReviewerIsNoted(t *testing.T) {
	ok := &fakeReviewer{results: []ReviewResult{{Approved: true, Summary: "Code correct."}}}
	broken := &fakeReviewer{err: errors.New("chrome introuvable")}
	res, err := MultiReviewer{ok, broken}.Review(context.Background(), ReviewRequest{}, nil)
	if err != nil || !res.Approved || !strings.Contains(res.Summary, "chrome introuvable") {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}
