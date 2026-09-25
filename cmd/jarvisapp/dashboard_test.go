package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Ticket "Revoir ordre des sections" : le tableau de bord est la première
// page de l'application, avec une statistique par section, dans l'ordre de
// la navigation (Notes, Emails, Documents, Tâches, Tickets).

// dashNow : horloge fixée des tests (un vendredi).
func dashNow() time.Time { return time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local) }

// fullDashboard : toutes les sources actives, de quoi remplir les cinq
// cartes.
func fullDashboard() dashboardData {
	now := dashNow()
	return dashboardData{
		Docs:    docCounts{Total: 12, Active: 1, Failed: 2},
		Mails:   mailCounts{Total: 40, Reply: 3, Untriaged: 7},
		Notes:   []notes.Note{{ID: "n1", Pinned: true}, {ID: "n2"}},
		Tasks:   []notes.Task{{ID: "t1", Due: notes.DateOf(now.AddDate(0, 0, -2))}, {ID: "t2", Due: notes.DateOf(now)}, {ID: "t3", Done: true}},
		Tickets: []tickets.Ticket{{ID: "k1", Status: tickets.PlanReady}, {ID: "k2", Status: tickets.Developing}, {ID: "k3", Status: tickets.Failed}, {ID: "k4", Status: tickets.Deployed}, {ID: "k5", Status: tickets.Cancelled}},
		Recent: []webapp.Job{
			{ID: "j1", Filename: "facture.pdf", Status: webapp.StatusDone, DocType: "facture", Tags: []string{"urgent"}, CreatedAt: now.Add(-time.Hour)},
		},
		NotesOn: true, MailOn: true, TicketsOn: true,
	}
}

// card : la carte de libellé label, si elle existe.
func card(t *testing.T, cards []templates.StatCard, label string) templates.StatCard {
	t.Helper()
	for _, c := range cards {
		if c.Label == label {
			return c
		}
	}
	t.Fatalf("aucune carte %q parmi %v", label, cardLabels(cards))
	return templates.StatCard{}
}

func cardLabels(cards []templates.StatCard) []string {
	var out []string
	for _, c := range cards {
		out = append(out, c.Label)
	}
	return out
}

func TestBuildDashboard_OneCardPerSectionInOrder(t *testing.T) {
	v := buildDashboard(dashNow(), fullDashboard())

	wantLabels := []string{"Notes", "Emails", "Documents", "Tâches", "Tickets"}
	if got := cardLabels(v.Cards); strings.Join(got, ",") != strings.Join(wantLabels, ",") {
		t.Fatalf("cartes = %v, want %v (l'ordre de la navigation)", got, wantLabels)
	}
	wantHrefs := []string{"/notes", "/emails", "/documents", "/tasks", "/tickets"}
	for i, want := range wantHrefs {
		if v.Cards[i].Href != want {
			t.Errorf("carte %s: Href = %q, want %q", v.Cards[i].Label, v.Cards[i].Href, want)
		}
	}

	// Valeurs : ce que chaque section compte réellement.
	for _, tc := range []struct{ label, value string }{
		{"Notes", "2"},
		{"Emails", "40"},
		{"Documents", "12"},
		{"Tâches", "2"},  // t3 est terminée
		{"Tickets", "3"}, // ni déployé ni annulé
	} {
		if got := card(t, v.Cards, tc.label).Value; got != tc.value {
			t.Errorf("carte %s: Value = %q, want %q", tc.label, got, tc.value)
		}
	}

	// Détails : les sous-totaux qui font agir.
	for _, tc := range []struct {
		label string
		want  []string
	}{
		{"Notes", []string{"épinglée"}},
		{"Emails", []string{"3 à répondre", "7 en cours d'analyse"}},
		{"Documents", []string{"1 en traitement", "2 en échec"}},
		{"Tâches", []string{"1 en retard", "1 aujourd'hui"}},
		{"Tickets", []string{"1 à valider", "1 en cours", "1 en échec"}},
	} {
		detail := card(t, v.Cards, tc.label).Detail
		for _, want := range tc.want {
			if !strings.Contains(detail, want) {
				t.Errorf("carte %s: Detail = %q, want contenant %q", tc.label, detail, want)
			}
		}
	}
}

// Un service désactivé n'affiche pas sa carte : un « 0 » laisserait croire
// qu'on a compté, alors qu'on ne sait rien.
func TestBuildDashboard_SkipsCardsOfDisabledServices(t *testing.T) {
	for _, tc := range []struct {
		name    string
		off     func(*dashboardData)
		absent  []string
		present []string
	}{
		{"notes et tâches", func(d *dashboardData) { d.NotesOn = false }, []string{"Notes", "Tâches"}, []string{"Documents", "Emails", "Tickets"}},
		{"emails", func(d *dashboardData) { d.MailOn = false }, []string{"Emails"}, []string{"Documents", "Notes", "Tâches", "Tickets"}},
		{"tickets", func(d *dashboardData) { d.TicketsOn = false }, []string{"Tickets"}, []string{"Documents", "Notes", "Tâches", "Emails"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fullDashboard()
			tc.off(&d)
			v := buildDashboard(dashNow(), d)
			labels := strings.Join(cardLabels(v.Cards), ",")
			for _, absent := range tc.absent {
				if strings.Contains(labels, absent) {
					t.Errorf("%s désactivé : la carte %s est affichée (%s)", tc.name, absent, labels)
				}
			}
			for _, present := range tc.present {
				if !strings.Contains(labels, present) {
					t.Errorf("%s désactivé : la carte %s a disparu (%s)", tc.name, present, labels)
				}
			}
		})
	}
}

func TestBuildDashboard_FlagsOverdueTasksAndFailedDocuments(t *testing.T) {
	v := buildDashboard(dashNow(), fullDashboard())
	for _, label := range []string{"Tâches", "Documents", "Emails"} {
		if !card(t, v.Cards, label).Alert {
			t.Errorf("carte %s: Alert = false, want true (retard / échec / réponse attendue)", label)
		}
	}
	if card(t, v.Cards, "Notes").Alert {
		t.Errorf("carte Notes: Alert = true, want false (rien d'urgent dans des notes)")
	}

	// Rien à signaler : aucune mise en avant.
	calm := fullDashboard()
	calm.Docs = docCounts{Total: 3}
	calm.Mails = mailCounts{Total: 3}
	calm.Tasks = []notes.Task{{ID: "t", Due: notes.DateOf(dashNow().AddDate(0, 0, 3))}}
	calm.Tickets = []tickets.Ticket{{ID: "k", Status: tickets.Draft}}
	for _, c := range buildDashboard(dashNow(), calm).Cards {
		if c.Alert {
			t.Errorf("carte %s: Alert = true alors que rien n'est en retard ni en échec", c.Label)
		}
	}
}

func TestBuildDashboard_ListsRecentDocuments(t *testing.T) {
	d := fullDashboard()
	d.Recent = append(d.Recent, webapp.Job{ID: "j2", Filename: "note.txt", Status: webapp.StatusFailed, Format: "text", CreatedAt: dashNow().Add(-2 * time.Hour)})
	v := buildDashboard(dashNow(), d)

	if len(v.Recent) != 2 {
		t.Fatalf("Recent = %d documents, want 2", len(v.Recent))
	}
	// L'ordre reçu du store (déjà trié du plus récent au plus ancien) est
	// conservé tel quel.
	if v.Recent[0].ID != "j1" || v.Recent[1].ID != "j2" {
		t.Errorf("Recent = %s, %s, want j1, j2 dans l'ordre reçu", v.Recent[0].ID, v.Recent[1].ID)
	}
	first := v.Recent[0]
	if first.Filename != "facture.pdf" || first.DocType != "facture" || first.Status != string(webapp.StatusDone) {
		t.Errorf("Recent[0] = %+v, want les métadonnées du job", first)
	}
	if len(first.Tags) != 1 || first.Tags[0] != "urgent" {
		t.Errorf("Recent[0].Tags = %v, want [urgent]", first.Tags)
	}
	if first.Ext != "PDF" || !first.HasThumbnail {
		t.Errorf("Recent[0] ext=%q thumbnail=%v, want PDF avec miniature", first.Ext, first.HasThumbnail)
	}
	// Heure locale (MongoDB rend les dates en UTC — correctif des jalons 26-27).
	if want := dashNow().Add(-time.Hour).Local().Format("02/01 15:04"); first.CreatedAt != want {
		t.Errorf("Recent[0].CreatedAt = %q, want %q", first.CreatedAt, want)
	}
	if v.Recent[1].Ext != "TXT" || v.Recent[1].Status != string(webapp.StatusFailed) {
		t.Errorf("Recent[1] = %+v, want un txt en échec", v.Recent[1])
	}
}

// Les compteurs d'emails viennent bien des trois requêtes de comptage, pas
// d'une liste tronquée.
func TestBuildDashboard_EmailDetailStaysSilentWithoutBacklog(t *testing.T) {
	d := fullDashboard()
	d.Mails = mailCounts{Total: 12, Reply: 0, Untriaged: 0}
	detail := card(t, buildDashboard(dashNow(), d).Cards, "Emails").Detail
	if strings.Contains(detail, "à répondre") || strings.Contains(detail, "analyse") {
		t.Errorf("Detail = %q, want aucun sous-total quand il n'y a rien à traiter", detail)
	}
}

// Une source illisible le dit : jamais un « 0 » fabriqué qui laisserait
// croire que la base est vide.
func TestBuildDashboard_SaysWhenASourceIsUnreadable(t *testing.T) {
	d := fullDashboard()
	d.DocsErr, d.NotesErr, d.TasksErr, d.MailErr, d.TicketsErr = true, true, true, true, true
	d.Docs, d.Mails, d.Notes, d.Tasks, d.Tickets = docCounts{}, mailCounts{}, nil, nil, nil
	v := buildDashboard(dashNow(), d)

	for _, label := range []string{"Notes", "Emails", "Documents", "Tâches", "Tickets"} {
		c := card(t, v.Cards, label)
		if c.Value == "0" || c.Value == "" {
			t.Errorf("carte %s: Value = %q, want un « — » plutôt qu'un zéro inventé", label, c.Value)
		}
		if !strings.Contains(c.Detail, "lecture impossible") {
			t.Errorf("carte %s: Detail = %q, want « lecture impossible »", label, c.Detail)
		}
	}

	// La grille des documents récents ne prétend pas non plus que la
	// bibliothèque est vide.
	d.RecentErr, d.Recent = true, nil
	if !buildDashboard(dashNow(), d).RecentErr {
		t.Errorf("RecentErr = false, want true (la liste n'a pas pu être lue)")
	}
}

// Les listes des stores sont bornées (notes.MaxListNotes,
// notes.MaxListTasks, tickets.MaxList) : au-delà, le compte est un
// minimum, pas un nombre exact.
func TestBuildDashboard_MarksCountsTruncatedByTheStores(t *testing.T) {
	d := fullDashboard()
	d.Notes = make([]notes.Note, notes.MaxListNotes)
	d.Tasks = make([]notes.Task, notes.MaxListTasks)
	d.Tickets = make([]tickets.Ticket, tickets.MaxList)
	v := buildDashboard(dashNow(), d)

	for _, label := range []string{"Notes", "Tâches", "Tickets"} {
		c := card(t, v.Cards, label)
		if !strings.HasSuffix(c.Value, "+") {
			t.Errorf("carte %s: Value = %q, want un compte marqué « + » (liste tronquée)", label, c.Value)
		}
		if !strings.Contains(c.Detail, "tronquée") {
			t.Errorf("carte %s: Detail = %q, want la mention de la troncature", label, c.Detail)
		}
	}

	// En deçà de la limite, le compte reste exact.
	for _, c := range buildDashboard(dashNow(), fullDashboard()).Cards {
		if strings.HasSuffix(c.Value, "+") {
			t.Errorf("carte %s: Value = %q, want un compte exact sous la limite", c.Label, c.Value)
		}
	}
}

// countingJobStore : un store de jobs illisible, qui compte les lectures
// reçues.
type countingJobStore struct {
	*webapp.FakeStore
	reads atomic.Int64
}

func (c *countingJobStore) List(context.Context, webapp.ListQuery) ([]webapp.Job, error) {
	c.reads.Add(1)
	return nil, errors.New("base injoignable")
}

func (c *countingJobStore) Count(context.Context, webapp.ListQuery) (int, error) {
	c.reads.Add(1)
	return 0, errors.New("base injoignable")
}

// « / » est le contrôle de santé du lanceur, avec 500 ms de budget par
// tentative (internal/launcher/health.go) : la page d'accueil ne lit
// aucune base — les statistiques arrivent ensuite par HTMX. Une page qui
// enchaînerait dix allers-retours Atlas ferait échouer le démarrage et le
// redémarrage après un retour arrière de déploiement.
func TestHandleDashboard_HomePageRendersWithoutReadingAnyStore(t *testing.T) {
	s, _ := spacesServerWithStore(t)
	jobs := &countingJobStore{FakeStore: webapp.NewFakeStore()}
	s.Jobs = webapp.NewJobManager(jobs, &blockingRunner{})

	body := page(t, s, "/")
	if n := jobs.reads.Load(); n != 0 {
		t.Errorf("GET / a fait %d lecture(s) de la base, want 0 (contrôle de santé du lanceur)", n)
	}
	if !strings.Contains(body, "Tableau de bord") {
		t.Errorf("la page d'accueil n'est pas le tableau de bord")
	}
	if !strings.Contains(body, `hx-get="/dashboard/stats"`) {
		t.Errorf("les statistiques ne sont pas chargées à part : %s", body)
	}
	// Aucun sondage périodique : le lanceur martèle « / » au démarrage, et
	// la barre latérale se rafraîchit déjà toute seule.
	if strings.Contains(strings.ReplaceAll(body, `hx-trigger="load, every 15s"`, ""), "every ") {
		t.Errorf("la page d'accueil se sonde périodiquement, want aucun sondage")
	}
}

// Les statistiques elles-mêmes (fragment chargé après coup).
func TestHandleDashboardStats_ShowsCounts(t *testing.T) {
	s, store := spacesServerWithStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, webapp.Job{ID: "doc-ok", Filename: "facture.pdf", Status: webapp.StatusDone, DocType: "facture", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, webapp.Job{ID: "doc-ko", Filename: "cassé.docx", Status: webapp.StatusFailed, Format: "word", Err: "conversion impossible", CreatedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	overdue := notes.Task{ID: "t1", Title: "payer le loyer", Due: notes.DateOf(time.Now().AddDate(0, 0, -2)), CreatedAt: time.Now()}
	if err := s.Notes.Store.CreateTask(ctx, overdue); err != nil {
		t.Fatal(err)
	}

	body := page(t, s, "/dashboard/stats")
	for _, want := range []string{"Documents", "Tâches", "facture.pdf", `href="/documents/doc-ok"`} {
		if !strings.Contains(body, want) {
			t.Errorf("les statistiques ne contiennent pas %q", want)
		}
	}
	// 2 documents comptés, dont 1 en échec.
	if !strings.Contains(body, "1 en échec") {
		t.Errorf("la carte Documents ne signale pas le document en échec")
	}
	// Un fragment, pas une page entière (il remplace le contenu du bloc).
	if strings.Contains(body, "<html") {
		t.Errorf("le fragment renvoie une page complète")
	}
}

// Une source illisible n'empêche jamais le fragment de s'afficher, et ne
// se présente jamais comme un « 0 ».
func TestHandleDashboardStats_StaysUpWhenAStoreFails(t *testing.T) {
	s, _ := spacesServerWithStore(t)
	s.Jobs = webapp.NewJobManager(&countingJobStore{FakeStore: webapp.NewFakeStore()}, &blockingRunner{})

	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/stats = %d, want 200 même avec une source illisible", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Notes", "Tickets", "lecture impossible"} {
		if !strings.Contains(body, want) {
			t.Errorf("le fragment a perdu %q alors qu'une seule source est en panne", want)
		}
	}
	if strings.Contains(body, "Aucun document pour l'instant") {
		t.Errorf("la base est illisible, mais le fragment annonce une bibliothèque vide")
	}
}
