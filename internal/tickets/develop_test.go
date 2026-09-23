package tickets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// --- Jalon 28 : l'agent développe, Jarvis vérifie, l'utilisateur relit ---

type fakeDeveloper struct {
	mu        sync.Mutex
	summaries []string
	err       error
	got       []DevRequest
}

func (d *fakeDeveloper) Develop(ctx context.Context, req DevRequest, onStep func(AgentStep)) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, req)
	onStep(AgentStep{Summary: "Modifie store.go"})
	if d.err != nil {
		return "", d.err
	}
	s := d.summaries[0]
	if len(d.summaries) > 1 {
		d.summaries = d.summaries[1:]
	}
	return s, nil
}

func (d *fakeDeveloper) Requests() []DevRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]DevRequest(nil), d.got...)
}

type fakeWorkspace struct {
	mu        sync.Mutex
	diff      string
	commits   []string
	discarded []string
}

func (w *fakeWorkspace) Prepare(ctx context.Context, id string) (string, error) {
	return "/ws/" + id, nil
}
func (w *fakeWorkspace) Diff(ctx context.Context, dir string) (string, error) { return w.diff, nil }
func (w *fakeWorkspace) Commit(ctx context.Context, dir, msg string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commits = append(w.commits, msg)
	return nil
}
func (w *fakeWorkspace) Discard(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.discarded = append(w.discarded, id)
	return nil
}

// fakeVerifier rend les verdicts dans l'ordre (vérifications puis tests,
// par tentative).
type fakeVerifier struct {
	mu      sync.Mutex
	results []bool
}

func (v *fakeVerifier) next() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	r := v.results[0]
	if len(v.results) > 1 {
		v.results = v.results[1:]
	}
	return r
}
func (v *fakeVerifier) Checks(ctx context.Context, dir string) (string, bool) {
	ok := v.next()
	return map[bool]string{true: "gofmt : ok", false: "go vet : échec\nstore.go:3: unreachable"}[ok], ok
}
func (v *fakeVerifier) Tests(ctx context.Context, dir, pkg string) (string, bool) {
	ok := v.next()
	return map[bool]string{true: "ok  tous les paquets", false: "--- FAIL: TestSince"}[ok], ok
}

func approvedTicket(t *testing.T, dev Developer, ws Workspace, v Verifier) (*Manager, *FakeStore, Ticket) {
	t.Helper()
	m, s := newManager(&fakeAnalyst{plan: "## Étapes\n1. x"})
	m.Developer, m.Workspace, m.Verifier = dev, ws, v
	tk, _ := m.Create(context.Background(), "Filtre par date", "besoin", "critère")
	m.StartAnalysis(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, PlanReady)
	return m, s, tk
}

// Valider le plan lance le développement ; vérification verte -> commit
// sur la branche du ticket et diff soumis à la revue.
func TestManager_ApprovePlanDevelopsVerifiesAndSubmitsDiff(t *testing.T) {
	dev := &fakeDeveloper{summaries: []string{"Ajout de ListQuery.Since"}}
	ws := &fakeWorkspace{diff: "+\tSince string"}
	m, s, tk := approvedTicket(t, dev, ws, &fakeVerifier{results: []bool{true}})

	if err := m.ApprovePlan(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	review := waitTicket(t, s, tk.ID, Review)
	if review.Diff != "+\tSince string" || review.Branch != "ticket/"+tk.ID || !strings.Contains(review.Report, "ok  tous les paquets") {
		t.Errorf("review ticket: diff %q branch %q report %q", review.Diff, review.Branch, review.Report)
	}
	if len(ws.commits) != 1 || !strings.Contains(ws.commits[0], "Filtre par date") || !strings.Contains(ws.commits[0], "Ajout de ListQuery.Since") {
		t.Errorf("commits = %v", ws.commits)
	}
	req := dev.Requests()[0]
	if req.Plan != "## Étapes\n1. x" || req.Dir != "/ws/"+tk.ID || req.Acceptance != "critère" {
		t.Errorf("dev request = %+v", req)
	}
}

// Vérification en échec : l'agent reçoit le rapport pour une seconde
// tentative, puis le ticket échoue avec la raison.
func TestManager_FailedVerificationRetriesWithReportThenFails(t *testing.T) {
	dev := &fakeDeveloper{summaries: []string{"essai 1", "essai 2"}}
	m, s, tk := approvedTicket(t, dev, &fakeWorkspace{diff: "+x"}, &fakeVerifier{results: []bool{false}})
	m.ApprovePlan(context.Background(), tk.ID)
	failed := waitTicket(t, s, tk.ID, Failed)

	reqs := dev.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1].Feedback, "unreachable") {
		t.Errorf("second attempt feedback = %+v, want the verification report", reqs)
	}
	last := failed.Events[len(failed.Events)-1]
	if last.Kind != EventError || !strings.Contains(last.Detail+last.Text, "unreachable") {
		t.Errorf("last event = %+v", last)
	}
}

func TestManager_SecondAttemptCanSucceed(t *testing.T) {
	dev := &fakeDeveloper{summaries: []string{"essai 1", "essai 2"}}
	// tentative 1 : vérifications KO ; tentative 2 : vérifications OK, tests OK.
	m, s, tk := approvedTicket(t, dev, &fakeWorkspace{diff: "+x"}, &fakeVerifier{results: []bool{false, true, true}})
	m.ApprovePlan(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Review)
}

func TestManager_EmptyDiffIsAFailure(t *testing.T) {
	m, s, tk := approvedTicket(t, &fakeDeveloper{summaries: []string{"rien"}}, &fakeWorkspace{diff: ""}, &fakeVerifier{results: []bool{true}})
	m.ApprovePlan(context.Background(), tk.ID)
	failed := waitTicket(t, s, tk.ID, Failed)
	if !strings.Contains(failed.Events[len(failed.Events)-1].Text, "aucune modification") {
		t.Errorf("events = %+v", failed.Events)
	}
}

func TestManager_DeveloperErrorFails(t *testing.T) {
	m, s, tk := approvedTicket(t, &fakeDeveloper{err: errors.New("modèle muet")}, &fakeWorkspace{diff: "+x"}, &fakeVerifier{results: []bool{true}})
	m.ApprovePlan(context.Background(), tk.ID)
	failed := waitTicket(t, s, tk.ID, Failed)
	if !strings.Contains(failed.Events[len(failed.Events)-1].Text, "modèle muet") {
		t.Errorf("events = %+v", failed.Events)
	}
}

// Revue : demander des changements relance le développement avec le
// retour ; accepter clôt la revue (le déploiement vient au jalon 29) ;
// abandonner supprime la copie de travail.
func TestManager_ReviewActions(t *testing.T) {
	dev := &fakeDeveloper{summaries: []string{"v1", "v2"}}
	ws := &fakeWorkspace{diff: "+x"}
	m, s, tk := approvedTicket(t, dev, ws, &fakeVerifier{results: []bool{true}})
	m.ApprovePlan(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Review)

	if err := m.RequestChanges(context.Background(), tk.ID, "renomme Since en From"); err != nil {
		t.Fatal(err)
	}
	waitTicket(t, s, tk.ID, Review)
	if reqs := dev.Requests(); !strings.Contains(reqs[len(reqs)-1].Feedback, "renomme Since en From") {
		t.Errorf("changes feedback not passed: %+v", reqs[len(reqs)-1])
	}

	if err := m.AcceptChanges(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(context.Background(), tk.ID); got.Status != Accepted {
		t.Errorf("status = %s", got.Status)
	}

	m2, s2, tk2 := approvedTicket(t, &fakeDeveloper{summaries: []string{"v1"}}, ws, &fakeVerifier{results: []bool{true}})
	m2.ApprovePlan(context.Background(), tk2.ID)
	waitTicket(t, s2, tk2.ID, Review)
	if err := m2.Cancel(context.Background(), tk2.ID); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ws.discarded {
		found = found || id == tk2.ID
	}
	if !found {
		t.Errorf("cancelled ticket workspace not discarded: %v", ws.discarded)
	}
}

// Sans développeur configuré, valider le plan s'arrête là (jalon 27).
func TestManager_ApproveWithoutDeveloperStaysApproved(t *testing.T) {
	m, s := newManager(&fakeAnalyst{plan: "p"})
	tk, _ := m.Create(context.Background(), "T", "b", "")
	m.StartAnalysis(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, PlanReady)
	m.ApprovePlan(context.Background(), tk.ID)
	if got, _, _ := s.Get(context.Background(), tk.ID); got.Status != PlanApproved {
		t.Errorf("status = %s, want plan_valide", got.Status)
	}
}

func TestCanTransition_DevelopmentPhase(t *testing.T) {
	for _, c := range []struct{ from, to Status }{
		{PlanApproved, Developing}, {Developing, Review}, {Developing, Failed},
		{Review, Accepted}, {Review, Developing}, {Review, Cancelled}, {Failed, Developing},
	} {
		if !CanTransition(c.from, c.to) {
			t.Errorf("CanTransition(%s -> %s) = false", c.from, c.to)
		}
	}
	for _, c := range []struct{ from, to Status }{{Developing, Accepted}, {Accepted, Developing}, {PlanReady, Developing}} {
		if CanTransition(c.from, c.to) {
			t.Errorf("CanTransition(%s -> %s) = true", c.from, c.to)
		}
	}
}

// Sans aucun fichier modifié, inutile de lancer la vérification complète
// (une minute) : l'échec est immédiat et dit pourquoi.
func TestManager_NoChangeSkipsVerification(t *testing.T) {
	v := &countingVerifier{}
	m, s, tk := approvedTicket(t, &fakeDeveloper{summaries: []string{"rien"}}, &fakeWorkspace{diff: ""}, v)
	m.ApprovePlan(context.Background(), tk.ID)
	failed := waitTicket(t, s, tk.ID, Failed)
	if v.calls != 0 {
		t.Errorf("verification ran %d times for an unchanged workspace", v.calls)
	}
	if !strings.Contains(failed.Events[len(failed.Events)-1].Text, "aucun fichier") {
		t.Errorf("last event = %+v", failed.Events[len(failed.Events)-1])
	}
}

type countingVerifier struct{ calls int }

func (v *countingVerifier) Checks(ctx context.Context, dir string) (string, bool) {
	v.calls++
	return "", true
}
func (v *countingVerifier) Tests(ctx context.Context, dir, pkg string) (string, bool) {
	v.calls++
	return "", true
}

// Le numéro de tentative (tout au long de la vie du ticket, relances
// comprises) est transmis à l'agent.
func TestManager_AttemptNumberGrowsAcrossRetries(t *testing.T) {
	dev := &fakeDeveloper{summaries: []string{"a"}}
	m, s, tk := approvedTicket(t, dev, &fakeWorkspace{diff: "+x"}, &fakeVerifier{results: []bool{false}})
	m.MaxAttempts = 1
	m.ApprovePlan(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Failed)
	m.StartDevelopment(context.Background(), tk.ID, "")
	waitTicket(t, s, tk.ID, Failed)
	reqs := dev.Requests()
	if len(reqs) != 2 || reqs[0].Attempt != 1 || reqs[1].Attempt != 2 {
		t.Errorf("attempts = %+v", reqs)
	}
}

// TDD strict (CLAUDE.md) : vu en réel, l'agent a annoncé « tests écrits
// et validés » sans avoir touché un seul fichier de test. Un diff qui
// modifie du code Go sans aucun _test.go est renvoyé à l'agent, puis le
// ticket échoue s'il persiste.
func TestManager_GoChangeWithoutTestIsSentBack(t *testing.T) {
	dev := &fakeDeveloper{summaries: []string{"tests écrits et validés"}}
	diff := "diff --git a/internal/webapp/store.go b/internal/webapp/store.go\n--- a/internal/webapp/store.go\n+++ b/internal/webapp/store.go\n@@ -1 +1 @@\n+\tComment string\n"
	m, s, tk := approvedTicket(t, dev, &fakeWorkspace{diff: diff}, &fakeVerifier{results: []bool{true}})
	m.ApprovePlan(context.Background(), tk.ID)
	failed := waitTicket(t, s, tk.ID, Failed)

	reqs := dev.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1].Feedback, "_test.go") {
		t.Errorf("requests = %+v, want a second attempt asking for tests", reqs)
	}
	if last := failed.Events[len(failed.Events)-1]; !strings.Contains(last.Text, "test") {
		t.Errorf("last event = %+v, want the missing tests named", last)
	}
}

func TestManager_GoChangeWithTestGoesToReview(t *testing.T) {
	diff := "+++ b/internal/webapp/store.go\n+\tComment string\n+++ b/internal/webapp/store_test.go\n+func TestComment(t *testing.T) {}\n"
	m, s, tk := approvedTicket(t, &fakeDeveloper{summaries: []string{"ok"}}, &fakeWorkspace{diff: diff}, &fakeVerifier{results: []bool{true}})
	m.ApprovePlan(context.Background(), tk.ID)
	waitTicket(t, s, tk.ID, Review)
}

func TestUntestedGoChange(t *testing.T) {
	cases := map[string]bool{
		"+++ b/a.go\n":                            true,
		"+++ b/a.go\n+++ b/a_test.go\n":           false,
		"+++ b/page.templ\n":                      false, // gabarit : pas de code Go testable seul
		"+++ b/page.templ\n+++ b/page_templ.go\n": false, // généré par templ
		"+++ b/CLAUDE.md\n":                       false,
		"--- a/old.go\n+++ /dev/null\n":           false, // suppression seule
		"+x":                                      false,
		"+++ b/cmd/x/main.go\n+++ b/x_test.go\n":  false,
	}
	for diff, want := range cases {
		if got := untestedGoChange(diff); got != want {
			t.Errorf("untestedGoChange(%q) = %v, want %v", diff, got, want)
		}
	}
}
