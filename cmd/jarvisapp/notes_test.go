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

// Crée une note et retourne son identifiant et celui de sa première
// boîte (la création ouvre directement cette boîte : ?edit=<bid>).
func createNote(t *testing.T, s *Server, form url.Values) (string, string) {
	t.Helper()
	rec := do(s, http.MethodPost, "/notes", form)
	loc := rec.Header().Get("Location")
	id, edit, ok := strings.Cut(strings.TrimPrefix(loc, "/notes/"), "?edit=")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/notes/") || !ok || edit == "" {
		t.Fatalf("create note: %d %q", rec.Code, loc)
	}
	return id, edit
}

// blockIDs : les identifiants des boîtes d'une note, dans l'ordre.
func blockIDs(t *testing.T, store *notes.FakeStore, id string) []string {
	t.Helper()
	n, ok, err := store.GetNote(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetNote(%s) = %v, %v", id, ok, err)
	}
	var out []string
	for _, b := range n.Blocks {
		out = append(out, b.ID)
	}
	return out
}

func TestNotes_CreateEditViewAndSearch(t *testing.T) {
	s, _ := newNotesServer(t)
	id, bid := createNote(t, s, url.Values{})
	if page := do(s, http.MethodGet, "/notes/"+id+"?edit="+bid, nil).Body.String(); !strings.Contains(page, `name="text"`) {
		t.Fatal("edit mode has no editor")
	}
	if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid, url.Values{"text": {"## Liste\n- **pain**\n- <script>x</script>"}}); rec.Code != http.StatusOK {
		t.Fatalf("save block: %d", rec.Code)
	}
	rec := do(s, http.MethodPost, "/notes/"+id, url.Values{"title": {"Courses"}, "tags": {"maison, urgent"}, "pinned": {"on"}})
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
	id, _ := createNote(t, s, url.Values{})
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
	id, bid := createNote(t, s, url.Values{"doc": {"doc-devis"}})
	do(s, http.MethodPost, "/notes/"+id, url.Values{"title": {"Questions sur le devis"}})
	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid, url.Values{"text": {"x"}})
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

// Autant de boîtes que voulu, réordonnables et supprimables ; la
// dernière supprimée laisse une boîte vide (une note en a toujours une).
func TestNotes_BoxesAddMoveDelete(t *testing.T) {
	s, store := newNotesServer(t)
	id, first := createNote(t, s, url.Values{})
	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+first, url.Values{"text": {"Première boîte"}})
	for _, text := range []string{"Deuxième boîte", "Troisième boîte"} {
		if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks", url.Values{}); rec.Code != http.StatusOK {
			t.Fatalf("add block: %d", rec.Code)
		}
		ids := blockIDs(t, store, id)
		do(s, http.MethodPost, "/notes/"+id+"/blocks/"+ids[len(ids)-1], url.Values{"text": {text}})
	}
	ids := blockIDs(t, store, id)
	if len(ids) != 3 {
		t.Fatalf("boîtes = %d, want 3", len(ids))
	}
	frag := do(s, http.MethodGet, "/notes/"+id+"/blocks", nil).Body.String()
	last := -1
	for _, want := range []string{"Première boîte", "Deuxième boîte", "Troisième boîte"} {
		i := strings.Index(frag, want)
		if i < 0 || i < last {
			t.Errorf("%q absente ou dans le désordre", want)
		}
		last = i
	}
	// Monter la troisième : elle passe devant la deuxième.
	if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+ids[2]+"/move", url.Values{"dir": {"up"}}); rec.Code != http.StatusOK {
		t.Fatalf("move: %d", rec.Code)
	}
	frag = do(s, http.MethodGet, "/notes/"+id+"/blocks", nil).Body.String()
	if strings.Index(frag, "Troisième boîte") > strings.Index(frag, "Deuxième boîte") {
		t.Error("↑ n'a pas changé l'ordre")
	}
	for _, b := range ids {
		if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+b+"/delete", url.Values{}); rec.Code != http.StatusOK {
			t.Fatalf("delete block: %d", rec.Code)
		}
	}
	n, _, _ := store.GetNote(context.Background(), id)
	if len(n.Blocks) != 1 || n.Blocks[0].Text != "" {
		t.Errorf("après suppression de toutes les boîtes = %+v", n.Blocks)
	}
}

// L'éditeur d'une boîte : barre d'outils Markdown et aperçu rendu par
// le serveur (rien n'est chargé depuis Internet).
func TestNotes_BoxEditorHasToolbarAndPreview(t *testing.T) {
	s, _ := newNotesServer(t)
	id, bid := createNote(t, s, url.Values{})
	page := do(s, http.MethodGet, "/notes/"+id+"?edit="+bid, nil).Body.String()
	for _, want := range []string{`data-md="bold"`, `name="text"`, `hx-post="/notes/blocks/preview"`} {
		if !strings.Contains(page, want) {
			t.Errorf("l'éditeur n'a pas %q", want)
		}
	}
	// Quitter la boîte l'enregistre (aucune saisie perdue en ouvrant une
	// autre boîte), mais circuler dedans (texte → tags → barre d'outils)
	// ne la referme pas.
	if !strings.Contains(page, "focusout[!this.contains(event.relatedTarget)]") {
		t.Error("l'éditeur ne s'enregistre pas quand le focus le quitte")
	}
	// Les boutons de la barre d'outils ne prennent pas le focus : sinon
	// cliquer sur « gras » refermerait l'éditeur.
	if !strings.Contains(page, `onmousedown="event.preventDefault()"`) {
		t.Error("les boutons de la barre d'outils prennent le focus")
	}
}

func TestNotes_MarkdownPreviewIsEscaped(t *testing.T) {
	s, _ := newNotesServer(t)
	rec := do(s, http.MethodPost, "/notes/blocks/preview", url.Values{"text": {"**gras** <script>x</script>"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<strong>gras</strong>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("aperçu = %q", body)
	}
	if strings.Contains(body, "<script>x") {
		t.Error("l'aperçu n'est pas échappé")
	}
}

func TestNotes_BoxToTask(t *testing.T) {
	s, store := newNotesServer(t)
	id, bid := createNote(t, s, url.Values{})
	// Une boîte vide ne devient pas une tâche : le refus s'affiche.
	rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid+"/task", url.Values{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "vide") {
		t.Fatalf("boîte vide → tâche : %d %q", rec.Code, rec.Body.String())
	}
	if tasks, _ := store.ListTasks(context.Background(), notes.TaskQuery{}); len(tasks) != 0 {
		t.Fatalf("une tâche a été créée depuis une boîte vide : %+v", tasks)
	}

	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid, url.Values{"text": {"- Appeler le notaire demain !"}})
	if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid+"/task", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("→ tâche : %d", rec.Code)
	}
	tasks, _ := store.ListTasks(context.Background(), notes.TaskQuery{NoteID: id})
	if len(tasks) != 1 || tasks[0].Title != "Appeler le notaire" || tasks[0].Due != "2026-09-24" || tasks[0].Priority != notes.High {
		t.Fatalf("tâches = %+v", tasks)
	}
	if frag := do(s, http.MethodGet, "/notes/"+id+"/blocks", nil).Body.String(); !strings.Contains(frag, `href="/tasks/`+tasks[0].ID+`"`) {
		t.Error("la boîte ne renvoie pas à sa tâche")
	}
}

func TestNotes_ArchiveBoxNeedsATagAndHides(t *testing.T) {
	s, _ := newNotesServer(t)
	id, bid := createNote(t, s, url.Values{})
	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid, url.Values{"text": {"vieille idée"}})

	rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid+"/archive", url.Values{"tags": {""}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "au moins un tag") || !strings.Contains(rec.Body.String(), "vieille idée") {
		t.Fatalf("archive sans tag : %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid+"/archive", url.Values{"tags": {"archive"}}); rec.Code != http.StatusOK {
		t.Fatalf("archive : %d", rec.Code)
	}
	frag := do(s, http.MethodGet, "/notes/"+id+"/blocks", nil).Body.String()
	if strings.Contains(frag, "vieille idée") {
		t.Error("la boîte archivée reste visible par défaut")
	}
	if !strings.Contains(frag, "1 boîte archivée") {
		t.Errorf("le nombre de boîtes archivées manque : %q", frag)
	}
	shown := do(s, http.MethodGet, "/notes/"+id+"/blocks?archives=1", nil).Body.String()
	if !strings.Contains(shown, "vieille idée") {
		t.Error("?archives=1 ne montre pas la boîte archivée")
	}
	if rec := do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid+"/unarchive", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("unarchive : %d", rec.Code)
	}
	if frag := do(s, http.MethodGet, "/notes/"+id+"/blocks", nil).Body.String(); !strings.Contains(frag, "vieille idée") {
		t.Error("la boîte désarchivée n'est pas revenue")
	}
}

// Une boîte archivée reste trouvable par son tag, et ce tag est proposé
// en filtre.
func TestNotes_SearchByTagFindsAnArchivedBox(t *testing.T) {
	s, _ := newNotesServer(t)
	id, bid := createNote(t, s, url.Values{})
	do(s, http.MethodPost, "/notes/"+id, url.Values{"title": {"Idées 2025"}})
	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid, url.Values{"text": {"vieille idée"}})
	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid+"/archive", url.Values{"tags": {"archive"}})

	list := do(s, http.MethodGet, "/notes?tag=archive", nil).Body.String()
	if !strings.Contains(list, `href="/notes/`+id+`"`) {
		t.Error("le filtre par tag de boîte ne trouve pas la note")
	}
	if !strings.Contains(do(s, http.MethodGet, "/notes", nil).Body.String(), "tag=archive") {
		t.Error("le tag de boîte n'est pas proposé en filtre")
	}
}

func TestNotes_FilterByUpdatedDate(t *testing.T) {
	s, store := newNotesServer(t)
	ctx := context.Background()
	for _, n := range []notes.Note{
		{ID: "vieille", Title: "Note de mai", UpdatedAt: time.Date(2026, 5, 4, 9, 0, 0, 0, time.Local)},
		{ID: "recente", Title: "Note de septembre", Tags: []string{"maison"}, UpdatedAt: notesNow},
	} {
		if err := store.CreateNote(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	page := do(s, http.MethodGet, "/notes?from=2026-09-01&to=2026-09-30", nil).Body.String()
	if !strings.Contains(page, "Note de septembre") || strings.Contains(page, "Note de mai") {
		t.Error("le filtre par date de modification ne filtre pas")
	}
	if !strings.Contains(page, "Effacer") {
		t.Error("le lien Effacer manque quand un filtre est actif")
	}
	// Les dates survivent au clic sur une pastille de tag.
	if !strings.Contains(page, "from=2026-09-01") || !strings.Contains(page, "tag=maison") {
		t.Error("la pastille de tag ne garde pas les dates")
	}
	// La borne de fin est incluse : le 23/09 est trouvé par to=2026-09-23.
	if page := do(s, http.MethodGet, "/notes?to=2026-09-23", nil).Body.String(); !strings.Contains(page, "Note de septembre") {
		t.Error("la date de fin n'est pas incluse")
	}
	inverted := do(s, http.MethodGet, "/notes?from=2026-09-30&to=2026-09-01", nil).Body.String()
	if !strings.Contains(inverted, "précède la date de début") {
		t.Error("dates inversées : aucun message")
	}
	invalid := do(s, http.MethodGet, "/notes?from=30/09/2026", nil).Body.String()
	if !strings.Contains(invalid, "Date invalide") || !strings.Contains(invalid, "Note de mai") {
		t.Error("date invalide : message manquant, ou filtre appliqué quand même")
	}
}

// Enregistrer l'en-tête (titre, tags, épingle) ne vide pas les boîtes.
func TestNotes_HeaderSaveKeepsBoxes(t *testing.T) {
	s, store := newNotesServer(t)
	id, bid := createNote(t, s, url.Values{})
	do(s, http.MethodPost, "/notes/"+id+"/blocks/"+bid, url.Values{"text": {"à garder"}})
	do(s, http.MethodPost, "/notes/"+id, url.Values{"title": {"Courses"}, "tags": {"maison"}})

	n, _, _ := store.GetNote(context.Background(), id)
	if len(n.Blocks) != 1 || n.Blocks[0].Text != "à garder" || n.Title != "Courses" {
		t.Errorf("après enregistrement de l'en-tête = %+v, %q", n.Blocks, n.Title)
	}
	if page := do(s, http.MethodGet, "/notes/"+id, nil).Body.String(); !strings.Contains(page, "à garder") {
		t.Error("le texte a disparu de la page")
	}
}

// Une note inconnue : 404, pas un fragment vide.
func TestNotes_BlockActionOnUnknownNote(t *testing.T) {
	s, _ := newNotesServer(t)
	if rec := do(s, http.MethodPost, "/notes/absente/blocks", url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("note inconnue = %d, want 404", rec.Code)
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
