package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// --- Jalons 26-27 : tickets et agent d'analyse ---

type stubAnalyst struct{ plan string }

func (a stubAnalyst) Analyze(ctx context.Context, req tickets.AnalysisRequest, onStep func(tickets.AgentStep)) (string, error) {
	onStep(tickets.AgentStep{Summary: "Lit internal/webapp/store.go", Detail: "type ListQuery struct"})
	return a.plan, nil
}

func newTicketServer(t *testing.T, plan string) (*Server, *tickets.FakeStore) {
	t.Helper()
	store := tickets.NewFakeStore()
	s, _ := newTestServer(t, &blockingRunner{})
	s.Tickets = &tickets.Manager{Store: store, Analyst: stubAnalyst{plan: plan}}
	return s, store
}

func postForm(t *testing.T, s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func waitTicketStatus(t *testing.T, store *tickets.FakeStore, id string, want tickets.Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tk, _, _ := store.Get(context.Background(), id); tk.Status == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("ticket %s never reached %s", id, want)
}

func TestTickets_ListPageWithCreationForm(t *testing.T) {
	s, store := newTicketServer(t, "p")
	store.Create(context.Background(), tickets.Ticket{ID: "t1", Title: "Filtre par date", Status: tickets.PlanReady, CreatedAt: time.Now()})
	body := get(t, s, "/tickets").Body.String()
	for _, want := range []string{`class="active">Tickets`, `action="/tickets"`, `name="title"`, `name="need"`, `name="acceptance"`, "Filtre par date", "Plan à valider"} {
		if !strings.Contains(body, want) {
			t.Errorf("tickets page lacks %q", want)
		}
	}
}

func TestTickets_CreateRedirectsToTicketAndRejectsEmptyTitle(t *testing.T) {
	s, store := newTicketServer(t, "p")
	rec := postForm(t, s, "/tickets", url.Values{"title": {"Filtre par date"}, "need": {"Filtrer par date"}, "acceptance": {"?since= marche"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/tickets/") {
		t.Fatalf("create = %d %q, want a 303 to the ticket", rec.Code, rec.Header().Get("Location"))
	}
	list, _ := store.List(context.Background(), "")
	if len(list) != 1 || list[0].Need != "Filtrer par date" {
		t.Errorf("stored = %+v", list)
	}
	if rec := postForm(t, s, "/tickets", url.Values{"title": {""}}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty title = %d, want 400", rec.Code)
	}
}

func TestTickets_AnalyzeThenShowPlanStepsAndActions(t *testing.T) {
	s, store := newTicketServer(t, "## Étapes\n1. ajouter <script>alert(1)</script>")
	tk, _ := s.Tickets.Create(context.Background(), "Filtre", "besoin", "")

	draft := get(t, s, "/tickets/"+tk.ID).Body.String()
	if !strings.Contains(draft, `hx-post="/tickets/`+tk.ID+`/analyze"`) {
		t.Errorf("draft ticket should offer to start the analysis")
	}

	rec := postForm(t, s, "/tickets/"+tk.ID+"/analyze", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("analyze = %d %s", rec.Code, rec.Body.String())
	}
	waitTicketStatus(t, store, tk.ID, tickets.PlanReady)

	body := get(t, s, "/tickets/"+tk.ID).Body.String()
	for _, want := range []string{
		"<h4>Étapes</h4>",              // titres du plan rendus
		"&lt;script&gt;",               // plan du modèle échappé
		"Lit internal/webapp/store.go", // étape de l'agent dans le fil
		"type ListQuery struct",        // sa sortie, repliée
		`hx-post="/tickets/` + tk.ID + `/approve"`,
		`hx-post="/tickets/` + tk.ID + `/revise"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ticket page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("model output rendered unescaped")
	}
}

func TestTickets_RunningPanelPollsItself(t *testing.T) {
	s, store := newTicketServer(t, "p")
	store.Create(context.Background(), tickets.Ticket{ID: "t1", Title: "T", Status: tickets.Analyzing, CreatedAt: time.Now()})
	body := get(t, s, "/tickets/t1/panel").Body.String()
	if !strings.Contains(body, `hx-get="/tickets/t1/panel"`) {
		t.Errorf("analyzing panel should poll itself: %s", body)
	}
}

func TestTickets_ApproveReviseCommentDelete(t *testing.T) {
	s, store := newTicketServer(t, "plan")
	tk, _ := s.Tickets.Create(context.Background(), "T", "b", "")
	s.Tickets.StartAnalysis(context.Background(), tk.ID)
	waitTicketStatus(t, store, tk.ID, tickets.PlanReady)

	postForm(t, s, "/tickets/"+tk.ID+"/comment", url.Values{"text": {"pense aux tests"}})
	postForm(t, s, "/tickets/"+tk.ID+"/revise", url.Values{"feedback": {"garde la compatibilité"}})
	waitTicketStatus(t, store, tk.ID, tickets.PlanReady)
	got, _, _ := store.Get(context.Background(), tk.ID)
	texts := ""
	for _, e := range got.Events {
		texts += e.Text + "|"
	}
	if !strings.Contains(texts, "pense aux tests") || !strings.Contains(texts, "garde la compatibilité") {
		t.Errorf("events = %s", texts)
	}

	if rec := postForm(t, s, "/tickets/"+tk.ID+"/approve", nil); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d", rec.Code)
	}
	if body := get(t, s, "/tickets/"+tk.ID).Body.String(); !strings.Contains(body, "Plan validé") {
		t.Error("approved ticket should say so")
	}

	req := httptest.NewRequest(http.MethodDelete, "/tickets/"+tk.ID, nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Header().Get("HX-Redirect") != "/tickets" {
		t.Errorf("delete: HX-Redirect = %q", rec.Header().Get("HX-Redirect"))
	}
	if get(t, s, "/tickets/"+tk.ID).Code != http.StatusNotFound {
		t.Error("deleted ticket still reachable")
	}
}

func TestTickets_InvalidTransitionIsAClearError(t *testing.T) {
	s, _ := newTicketServer(t, "p")
	tk, _ := s.Tickets.Create(context.Background(), "T", "b", "")
	rec := postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "impossible") {
		t.Errorf("approving a draft = %d %q, want 409 with the reason", rec.Code, rec.Body.String())
	}
}

// Trouvé sur le premier ticket réel : MongoDB rend les dates en UTC, les
// pages les affichaient telles quelles (06:50 pour 08:50 à Paris). Toutes
// les heures affichées sont en heure locale.
func TestTimesAreShownInLocalTime(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("Paris", 2*3600)
	defer func() { time.Local = saved }()
	utc := time.Date(2026, 9, 23, 6, 50, 3, 0, time.UTC)

	s, store := newTicketServer(t, "p")
	store.Create(context.Background(), tickets.Ticket{ID: "t1", Title: "T", Status: tickets.Draft, CreatedAt: utc})
	store.AppendEvent(context.Background(), "t1", tickets.Event{At: utc, Kind: tickets.EventComment, Author: tickets.AuthorUser, Text: "x"})
	if body := get(t, s, "/tickets/t1").Body.String(); !strings.Contains(body, "08:50:03") || strings.Contains(body, "06:50:03") {
		t.Error("ticket thread time not shown in local time")
	}
	if body := get(t, s, "/tickets").Body.String(); !strings.Contains(body, "23/09/2026 08:50") {
		t.Error("ticket list date not shown in local time")
	}

	s2, jobs := newTestServer(t, &blockingRunner{})
	seedFile(t, jobs, "d1", "a.pdf", "pdf", "application/pdf", []byte("%PDF"), nil)
	job, _, _ := jobs.Get(context.Background(), "d1")
	job.CreatedAt = utc
	jobs.Update(context.Background(), job)
	if body := get(t, s2, "/documents").Body.String(); !strings.Contains(body, "2026-09-23 08:50") {
		t.Error("documents grid date not shown in local time")
	}
	if body := get(t, s2, "/documents/d1").Body.String(); !strings.Contains(body, "23/09/2026 à 08:50") {
		t.Error("document detail date not shown in local time")
	}
}

// --- Jalon 28 : développement et revue du diff ---

type stubDeveloper struct{}

func (stubDeveloper) Develop(ctx context.Context, req tickets.DevRequest, onStep func(tickets.AgentStep)) (string, error) {
	onStep(tickets.AgentStep{Summary: "Modifie internal/webapp/store.go", Detail: "Modifié"})
	return "Ajout de ListQuery.Since", nil
}

type stubWorkspace struct{ diff string }

func (w stubWorkspace) Prepare(ctx context.Context, id string) (string, error) { return "/ws", nil }
func (w stubWorkspace) Diff(ctx context.Context, dir string) (string, error)   { return w.diff, nil }
func (stubWorkspace) Commit(ctx context.Context, dir, msg string) error        { return nil }
func (stubWorkspace) Discard(ctx context.Context, id string) error             { return nil }

type stubVerifier struct{ ok bool }

func (v stubVerifier) Checks(ctx context.Context, dir string) (string, bool) {
	return "gofmt : ok", v.ok
}
func (v stubVerifier) Tests(ctx context.Context, dir, pkg string) (string, bool) {
	return "ok  	github.com/x/internal/webapp	0.5s", v.ok
}

const sampleDiff = `diff --git a/internal/webapp/store.go b/internal/webapp/store.go
index 1111111..2222222 100644
--- a/internal/webapp/store.go
+++ b/internal/webapp/store.go
@@ -10,6 +10,7 @@ type ListQuery struct {
 	Search string
-	Limit  int
+	Limit  int
+	Since  string // <script>alert(1)</script>
 }
`

func newDevTicketServer(t *testing.T, verifyOK bool) (*Server, *tickets.FakeStore, tickets.Ticket) {
	t.Helper()
	s, store := newTicketServer(t, "## Étapes\n1. x")
	s.Tickets.Developer, s.Tickets.Workspace, s.Tickets.Verifier = stubDeveloper{}, stubWorkspace{diff: sampleDiff}, stubVerifier{ok: verifyOK}
	tk, _ := s.Tickets.Create(context.Background(), "Filtre par date", "b", "")
	s.Tickets.StartAnalysis(context.Background(), tk.ID)
	waitTicketStatus(t, store, tk.ID, tickets.PlanReady)
	return s, store, tk
}

func TestTickets_ApproveDevelopsThenShowsDiffForReview(t *testing.T) {
	s, store, tk := newDevTicketServer(t, true)
	postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Review)

	body := get(t, s, "/tickets/"+tk.ID).Body.String()
	for _, want := range []string{
		"Diff à valider",
		`class="diff-file"`, "internal/webapp/store.go",
		`class="diff-line diff-add"`, `class="diff-line diff-del"`, `class="diff-line diff-hunk"`,
		"&lt;script&gt;",                     // diff d'un modèle : échappé
		"ok  \tgithub.com/x/internal/webapp", // rapport de vérification
		`hx-post="/tickets/` + tk.ID + `/accept"`,
		`hx-post="/tickets/` + tk.ID + `/changes"`,
		"ticket/" + tk.ID, // branche
	} {
		if !strings.Contains(body, want) {
			t.Errorf("review page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("diff rendered unescaped")
	}
}

func TestTickets_AcceptAndRequestChanges(t *testing.T) {
	s, store, tk := newDevTicketServer(t, true)
	postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Review)

	if rec := postForm(t, s, "/tickets/"+tk.ID+"/changes", url.Values{"feedback": {"renomme Since en From"}}); rec.Code != http.StatusOK {
		t.Fatalf("changes = %d %s", rec.Code, rec.Body.String())
	}
	waitTicketStatus(t, store, tk.ID, tickets.Review)
	if rec := postForm(t, s, "/tickets/"+tk.ID+"/accept", nil); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d", rec.Code)
	}
	if body := get(t, s, "/tickets/"+tk.ID).Body.String(); !strings.Contains(body, "Accepté") || !strings.Contains(body, "sans déploiement") {
		t.Error("accepted ticket (no deployer) should say it was not deployed")
	}
}

func TestTickets_FailedDevelopmentOffersRetry(t *testing.T) {
	s, store, tk := newDevTicketServer(t, false)
	postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Failed)
	body := get(t, s, "/tickets/"+tk.ID).Body.String()
	if !strings.Contains(body, `hx-post="/tickets/`+tk.ID+`/develop"`) {
		t.Errorf("failed development should offer to retry the development")
	}
	if rec := postForm(t, s, "/tickets/"+tk.ID+"/develop", nil); rec.Code != http.StatusOK {
		t.Errorf("retry develop = %d %s", rec.Code, rec.Body.String())
	}
}

// --- Jalon 30 : déploiement ---

type stubDeployer struct{ err error }

func (d stubDeployer) Deploy(ctx context.Context, id string, onStep func(text, detail string)) error {
	onStep("Essai à blanc réussi", "")
	return d.err
}

func TestTickets_ReviewOffersAcceptAndDeploy(t *testing.T) {
	s, store, tk := newDevTicketServer(t, true)
	s.Tickets.Deployer = stubDeployer{}
	postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Review)

	body := get(t, s, "/tickets/"+tk.ID).Body.String()
	if !strings.Contains(body, "Accepter et déployer") || !strings.Contains(body, "hx-confirm=") {
		t.Error("review should offer accept-and-deploy, with a confirmation")
	}
	postForm(t, s, "/tickets/"+tk.ID+"/accept", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Deploying)
	body = get(t, s, "/tickets/"+tk.ID+"/panel").Body.String()
	for _, want := range []string{"Déploiement en cours", "essai à blanc", `hx-trigger="load delay:2s"`} {
		if !strings.Contains(body, want) {
			t.Errorf("deploying panel lacks %q", want)
		}
	}

	s.Tickets.ConfirmDeployment(context.Background(), tk.ID, "abcdef123")
	body = get(t, s, "/tickets/"+tk.ID+"/panel").Body.String()
	if !strings.Contains(body, "Déployé") || !strings.Contains(body, "git push") {
		t.Error("deployed panel should say so and remind that main is not pushed")
	}
}

func TestTickets_AcceptedTicketCanBeDeployedLater(t *testing.T) {
	s, store, tk := newDevTicketServer(t, true)
	postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Review)
	postForm(t, s, "/tickets/"+tk.ID+"/accept", nil) // pas encore de déployeur
	waitTicketStatus(t, store, tk.ID, tickets.Accepted)

	s.Tickets.Deployer = stubDeployer{}
	body := get(t, s, "/tickets/"+tk.ID).Body.String()
	if !strings.Contains(body, `hx-post="/tickets/`+tk.ID+`/deploy"`) {
		t.Fatal("accepted ticket with a branch should offer to deploy")
	}
	if rec := postForm(t, s, "/tickets/"+tk.ID+"/deploy", nil); rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d %s", rec.Code, rec.Body)
	}
	waitTicketStatus(t, store, tk.ID, tickets.Deploying)
}

type stubPusher struct{ pushed bool }

func (p *stubPusher) Push(ctx context.Context) (string, error) {
	p.pushed = true
	return "abc1234def", nil
}

func TestTickets_DeployedTicketOffersPushWithPendingCommits(t *testing.T) {
	s, store, tk := newDevTicketServer(t, true)
	s.Tickets.Deployer = stubDeployer{}
	pusher := &stubPusher{}
	s.Tickets.Pusher = pusher
	pending := []string{"abc1234 Ticket x : commentaires", "0ff1ce0 Commit fait à la main"}
	s.Unpushed = func(ctx context.Context) ([]string, error) {
		if pusher.pushed {
			return nil, nil
		}
		return pending, nil
	}
	postForm(t, s, "/tickets/"+tk.ID+"/approve", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Review)
	postForm(t, s, "/tickets/"+tk.ID+"/accept", nil)
	waitTicketStatus(t, store, tk.ID, tickets.Deploying)
	s.Tickets.ConfirmDeployment(context.Background(), tk.ID, "abc1234def")

	body := get(t, s, "/tickets/"+tk.ID).Body.String()
	for _, want := range []string{`hx-post="/tickets/` + tk.ID + `/push"`, "Pousser vers GitHub", "2 commit(s)", "Commit fait à la main", "hx-confirm="} {
		if !strings.Contains(body, want) {
			t.Errorf("deployed ticket lacks %q", want)
		}
	}
	rec := postForm(t, s, "/tickets/"+tk.ID+"/push", nil)
	if rec.Code != http.StatusOK || !pusher.pushed {
		t.Fatalf("push = %d, pushed=%v", rec.Code, pusher.pushed)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Poussé vers GitHub") || strings.Contains(body, `/push"`) {
		t.Error("after the push, the panel should say so and no longer offer the button")
	}
}
