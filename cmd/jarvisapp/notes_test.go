package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Mercredi 23 septembre 2026, 15 h.
var notesNow = time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)

func newNotesServer(t *testing.T) (*Server, *notes.FakeStore) {
	t.Helper()
	s, jobs := newTestServer(t, &blockingRunner{})
	jobs.Create(context.Background(), webapp.Job{ID: "doc-devis", Filename: "Devis toiture.pdf", Status: webapp.StatusDone, CreatedAt: notesNow})
	store := notes.NewFakeStore()
	s.Notes = &notes.Service{Store: store, Now: func() time.Time { return notesNow }}
	return s, store
}

func do(s *Server, method, target string, form url.Values) *httptest.ResponseRecorder {
	return serve(s, method, target, form)
}

// Crée une note et retourne son identifiant (lu dans la redirection).
func createNote(t *testing.T, s *Server, form url.Values) string {
	t.Helper()
	rec := do(s, http.MethodPost, "/notes", form)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/notes/") || !strings.HasSuffix(loc, "?edit=1") {
		t.Fatalf("create note: %d %q", rec.Code, loc)
	}
	return strings.TrimSuffix(strings.TrimPrefix(loc, "/notes/"), "?edit=1")
}

func TestNotes_CreateEditViewAndSearch(t *testing.T) {
	s, _ := newNotesServer(t)
	id := createNote(t, s, url.Values{})
	if page := do(s, http.MethodGet, "/notes/"+id+"?edit=1", nil).Body.String(); !strings.Contains(page, `name="body"`) {
		t.Fatal("edit mode has no editor")
	}
	rec := do(s, http.MethodPost, "/notes/"+id, url.Values{"title": {"Courses"}, "body": {"## Liste\n- **pain**\n- <script>x</script>"}, "tags": {"maison, urgent"}, "pinned": {"on"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notes/"+id {
		t.Fatalf("save: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	page := do(s, http.MethodGet, "/notes/"+id, nil).Body.String()
	for _, want := range []string{"Courses", "<strong>pain</strong>", "&lt;script&gt;", "maison", "📌"} {
		if !strings.Contains(page, want) {
			t.Errorf("note page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>x") {
		t.Error("note body not escaped")
	}
	list := do(s, http.MethodGet, "/notes?q=PAIN", nil).Body.String()
	if !strings.Contains(list, `href="/notes/`+id+`"`) || !strings.Contains(list, `href="/notes"`) {
		t.Error("search by body does not find the note, or nav lacks Notes")
	}
	if list := do(s, http.MethodGet, "/notes?q=absent", nil).Body.String(); strings.Contains(list, "Courses") {
		t.Error("search shows a non-matching note")
	}
	if rec := do(s, http.MethodPost, "/notes/"+id+"/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do(s, http.MethodGet, "/notes/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleted note: %d", rec.Code)
	}
}

// Tâche créée depuis une note : visible dans la note, cochée sans quitter
// la note (retour à la page d'origine).
func TestNotes_TasksFromANote(t *testing.T) {
	s, store := newNotesServer(t)
	id := createNote(t, s, url.Values{})
	rec := do(s, http.MethodPost, "/tasks", url.Values{"text": {"Acheter du lait demain !"}, "note": {id}, "back": {"/notes/" + id}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notes/"+id {
		t.Fatalf("add task: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	tasks, _ := store.ListTasks(context.Background(), notes.TaskQuery{NoteID: id})
	if len(tasks) != 1 || tasks[0].Title != "Acheter du lait" || tasks[0].Due != "2026-09-24" || tasks[0].Priority != notes.High {
		t.Fatalf("tasks = %+v", tasks)
	}
	page := do(s, http.MethodGet, "/notes/"+id, nil).Body.String()
	if !strings.Contains(page, "Acheter du lait") || !strings.Contains(page, "demain") {
		t.Error("note page lacks its task or its due date")
	}
	// Dans sa note, une tâche ne renvoie pas à cette même note.
	if strings.Contains(page, `href="/notes/`+id+`"`) {
		t.Error("task on its own note page links back to the note")
	}
	rec = do(s, http.MethodPost, "/tasks/"+tasks[0].ID+"/toggle", url.Values{"back": {"/notes/" + id}})
	if rec.Header().Get("Location") != "/notes/"+id {
		t.Errorf("toggle redirect = %q", rec.Header().Get("Location"))
	}
	if tk, _, _ := store.GetTask(context.Background(), tasks[0].ID); !tk.Done {
		t.Error("task not done")
	}
}

func TestTasks_GroupedPage(t *testing.T) {
	s, store := newNotesServer(t)
	// La saisie rapide ne crée pas de tâche en retard (une date passée
	// part l'an prochain) : celle-ci est posée directement.
	store.CreateTask(context.Background(), notes.Task{ID: "edf", Title: "Payer EDF", Due: "2026-09-20", CreatedAt: notesNow})
	for _, text := range []string{"Courses aujourd'hui", "Réunion lundi", "Lire le rapport"} {
		do(s, http.MethodPost, "/tasks", url.Values{"text": {text}})
	}
	page := do(s, http.MethodGet, "/tasks", nil).Body.String()
	order := []string{"En retard", "Payer EDF", "Aujourd&#39;hui", "Courses", "À venir", "Réunion", "Sans échéance", "Lire le rapport", `href="/tasks"`}
	last := -1
	for _, want := range order {
		i := strings.Index(page, want)
		if want == `href="/tasks"` {
			if i < 0 {
				t.Error("nav lacks Tâches")
			}
			continue
		}
		if i < 0 || i < last {
			t.Errorf("%q missing or out of order", want)
		}
		last = i
	}
}

func TestTasks_EditValidatesAndLinks(t *testing.T) {
	s, store := newNotesServer(t)
	do(s, http.MethodPost, "/tasks", url.Values{"text": {"Relire"}})
	tasks, _ := store.ListTasks(context.Background(), notes.TaskQuery{})
	id := tasks[0].ID
	if page := do(s, http.MethodGet, "/tasks/"+id, nil).Body.String(); !strings.Contains(page, "Devis toiture.pdf") {
		t.Error("edit page does not offer the documents")
	}
	rec := do(s, http.MethodPost, "/tasks/"+id, url.Values{"title": {"Relire le devis"}, "due": {"31/12/2026"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "échéance invalide") || !strings.Contains(rec.Body.String(), "Relire le devis") {
		t.Errorf("invalid due: %d", rec.Code)
	}
	rec = do(s, http.MethodPost, "/tasks/"+id, url.Values{"title": {"Relire le devis"}, "due": {"2026-12-31"}, "priority": {"haute"}, "doc": {"doc-devis"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if tk, _, _ := store.GetTask(context.Background(), id); tk.DocID != "doc-devis" || tk.Due != "2026-12-31" {
		t.Errorf("task = %+v", tk)
	}
	if page := do(s, http.MethodGet, "/tasks", nil).Body.String(); !strings.Contains(page, `href="/documents/doc-devis"`) {
		t.Error("task list lacks the link to its document")
	}
}

// Une note liée à un document, et la fiche du document qui montre ses
// notes et tâches.
func TestNotes_DocumentLinksBothWays(t *testing.T) {
	s, store := newNotesServer(t)
	id := createNote(t, s, url.Values{"doc": {"doc-devis"}})
	do(s, http.MethodPost, "/notes/"+id, url.Values{"title": {"Questions sur le devis"}, "body": {"x"}})
	do(s, http.MethodPost, "/tasks", url.Values{"text": {"Appeler le couvreur"}, "doc": {"doc-devis"}})

	page := do(s, http.MethodGet, "/notes/"+id, nil).Body.String()
	if !strings.Contains(page, `href="/documents/doc-devis"`) || !strings.Contains(page, "Devis toiture.pdf") {
		t.Error("note page lacks its linked document")
	}
	links := do(s, http.MethodGet, "/documents/doc-devis/links", nil).Body.String()
	for _, want := range []string{"Questions sur le devis", "Appeler le couvreur", `name="doc" value="doc-devis"`} {
		if !strings.Contains(links, want) {
			t.Errorf("document links lack %q", want)
		}
	}
	if strings.Contains(links, `href="/documents/doc-devis"`) {
		t.Error("task on its own document page links back to the document")
	}
	if detail := do(s, http.MethodGet, "/documents/doc-devis", nil).Body.String(); !strings.Contains(detail, `/documents/doc-devis/links`) {
		t.Error("document page does not load its links")
	}
	do(s, http.MethodPost, "/notes/"+id+"/docs/doc-devis/unlink", url.Values{})
	if n, _, _ := store.GetNote(context.Background(), id); len(n.DocIDs) != 0 {
		t.Errorf("unlink: %v", n.DocIDs)
	}
	do(s, http.MethodPost, "/notes/"+id+"/docs", url.Values{"doc": {"doc-devis"}})
	if n, _, _ := store.GetNote(context.Background(), id); len(n.DocIDs) != 1 {
		t.Errorf("link: %v", n.DocIDs)
	}
}

// Le retour après une action reste dans l'application.
func TestTasks_BackRedirectStaysLocal(t *testing.T) {
	s, _ := newNotesServer(t)
	for _, back := range []string{"//evil.example", "https://evil.example", `/\evil.example`} {
		rec := do(s, http.MethodPost, "/tasks", url.Values{"text": {"x"}, "back": {back}})
		if loc := rec.Header().Get("Location"); loc != "/tasks" {
			t.Errorf("back %q → %q, want /tasks", back, loc)
		}
	}
}
