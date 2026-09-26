package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/projectinfo"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Notes et tâches (jalon 32) : prise de notes en Markdown et todo à
// échéances, liées entre elles et aux documents de la bibliothèque.

func (s *Server) notesRoutes(r chi.Router) {
	r.Get("/notes", s.handleNotes)
	r.Post("/notes", s.handleNoteCreate)
	r.Get("/notes/{id}", s.handleNote)
	r.Post("/notes/{id}", s.handleNoteSave)
	// Boîtes d'une note. « blocks/preview » a trois segments : aucune
	// collision avec POST /notes/{id}.
	r.Post("/notes/blocks/preview", s.handleNoteBlockPreview)
	r.Get("/notes/{id}/blocks", s.handleNoteBlocks)
	r.Post("/notes/{id}/blocks", s.handleNoteBlockAdd)
	r.Post("/notes/{id}/blocks/{bid}", s.handleNoteBlockSave)
	r.Post("/notes/{id}/blocks/{bid}/move", s.handleNoteBlockMove)
	r.Post("/notes/{id}/blocks/{bid}/delete", s.handleNoteBlockDelete)
	r.Post("/notes/{id}/blocks/{bid}/task", s.handleNoteBlockTask)
	r.Post("/notes/{id}/blocks/{bid}/archive", s.handleNoteBlockArchive)
	r.Post("/notes/{id}/blocks/{bid}/unarchive", s.handleNoteBlockUnarchive)
	r.Post("/notes/{id}/delete", s.handleNoteDelete)
	r.Post("/notes/{id}/docs", s.handleNoteLinkDoc)
	r.Post("/notes/{id}/docs/{doc}/unlink", s.handleNoteUnlinkDoc)
	r.Get("/tasks", s.handleTasks)
	r.Post("/tasks", s.handleTaskAdd)
	r.Get("/tasks/{id}", s.handleTaskEdit)
	r.Post("/tasks/{id}", s.handleTaskSave)
	r.Post("/tasks/{id}/toggle", s.handleTaskToggle)
	r.Post("/tasks/{id}/delete", s.handleTaskDelete)
	r.Get("/documents/{id}/links", s.handleDocumentLinks)
}

func (s *Server) notesEnabled(w http.ResponseWriter) bool {
	if s.Notes == nil {
		http.Error(w, "notes et tâches non configurées", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func renderPage(w http.ResponseWriter, r *http.Request, status int, c templ.Component, what string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render %s: %v\n", what, err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// localBack : la page où revenir après une action, seulement si elle est
// dans l'application (jamais « //autre-site » ni une URL absolue).
func localBack(back, fallback string) string {
	if strings.HasPrefix(back, "/") && !strings.HasPrefix(back, "//") && !strings.ContainsAny(back, "\\\r\n") {
		return back
	}
	return fallback
}

// --- Notes ---

func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	q := r.URL.Query()
	f := templates.NotesFilters{Search: q.Get("q"), Tag: q.Get("tag"), From: q.Get("from"), To: q.Get("to")}
	query := notes.NoteQuery{Search: f.Search, Tag: f.Tag}
	query.UpdatedFrom, query.UpdatedBefore, f.Error = dateRange(f.From, f.To, "aucune note ne peut correspondre")
	list, err := s.Notes.Store.ListNotes(r.Context(), query)
	if err != nil {
		serverError(w, err)
		return
	}
	all, err := s.Notes.Store.ListNotes(r.Context(), notes.NoteQuery{})
	if err != nil {
		serverError(w, err)
		return
	}
	renderPage(w, r, http.StatusOK, templates.NotesPage(noteCards(list), f, allTags(all)), "notes")
}

func (s *Server) handleNoteCreate(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	n, err := s.Notes.NewNote(r.Context(), r.FormValue("doc"))
	if err != nil {
		serverError(w, err)
		return
	}
	// La première boîte s'ouvre directement : il n'y a rien à lire.
	http.Redirect(w, r, "/notes/"+n.ID+"?edit="+n.Blocks[0].ID, http.StatusSeeOther)
}

func (s *Server) handleNote(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	ctx := r.Context()
	n, ok, err := s.Notes.Store.GetNote(ctx, chi.URLParam(r, "id"))
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	tasks, err := s.Notes.Store.ListTasks(ctx, notes.TaskQuery{NoteID: n.ID})
	if err != nil {
		serverError(w, err)
		return
	}
	names := s.docNames(ctx, n.DocIDs)
	v := templates.NoteView{
		MailSubject: s.mailSubjects(ctx, nonEmpty(n.MailID))[n.MailID],
		Note:        n,
		Blocks:      s.blockViews(ctx, n, r.URL.Query().Get("edit"), r.URL.Query().Get("archives") != ""),
		Tasks:       s.taskGroups(ctx, tasks, taskContext{noteID: n.ID}),
	}
	for _, id := range n.DocIDs {
		v.Docs = append(v.Docs, templates.DocLink{ID: id, Name: names[id]})
	}
	for _, d := range s.recentDocs(ctx) {
		if !contains(n.DocIDs, d.ID) {
			v.Choices = append(v.Choices, d)
		}
	}
	renderPage(w, r, http.StatusOK, templates.NotePage(v), "note")
}

func (s *Server) handleNoteSave(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.Notes.SaveNote(r.Context(), id, r.FormValue("title"), r.FormValue("tags"), r.FormValue("pinned") != ""); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/notes/"+id, http.StatusSeeOther)
}

// --- Boîtes d'une note ---
//
// Toutes les actions de boîte rendent le même fragment : la colonne
// entière (#note-blocks). Un seul chemin de rendu, donc pas de
// divergence entre fragments — et un refus (archive sans tag, boîte
// vide) revient avec le statut 200, le message porté par le fragment :
// HTMX ne remplacerait pas la cible sur une réponse 4xx.

// blockAction exécute fn puis rend la colonne de boîtes. fn retourne la
// boîte à ouvrir dans l'éditeur ensuite ("" : aucune).
func (s *Server) blockAction(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, noteID, blockID string) (string, error)) {
	if !s.notesEnabled(w) {
		return
	}
	ctx := r.Context()
	noteID, blockID := chi.URLParam(r, "id"), chi.URLParam(r, "bid")
	n, ok, err := s.Notes.Store.GetNote(ctx, noteID)
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	editID, err := fn(ctx, noteID, blockID)
	var msg string
	if err != nil {
		msg, editID = err.Error(), ""
	}
	if n, ok, err = s.Notes.Store.GetNote(ctx, noteID); err != nil {
		serverError(w, err)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	v := s.blockViews(ctx, n, editID, r.URL.Query().Get("archives") != "")
	v.Error = msg
	renderPage(w, r, http.StatusOK, templates.NoteBlocks(v), "note blocks")
}

func (s *Server) handleNoteBlocks(w http.ResponseWriter, r *http.Request) {
	edit := r.URL.Query().Get("edit")
	s.blockAction(w, r, func(context.Context, string, string) (string, error) { return edit, nil })
}

func (s *Server) handleNoteBlockAdd(w http.ResponseWriter, r *http.Request) {
	after := r.FormValue("after")
	s.blockAction(w, r, func(ctx context.Context, noteID, _ string) (string, error) {
		// La boîte neuve est vide : elle s'ouvre directement.
		b, err := s.Notes.AddBlock(ctx, noteID, after)
		return b.ID, err
	})
}

func (s *Server) handleNoteBlockSave(w http.ResponseWriter, r *http.Request) {
	text, tags := r.FormValue("text"), r.FormValue("tags")
	s.blockAction(w, r, func(ctx context.Context, noteID, blockID string) (string, error) {
		_, err := s.Notes.SaveBlock(ctx, noteID, blockID, text, tags)
		return "", err
	})
}

func (s *Server) handleNoteBlockMove(w http.ResponseWriter, r *http.Request) {
	up := r.FormValue("dir") != "down"
	s.blockAction(w, r, func(ctx context.Context, noteID, blockID string) (string, error) {
		return "", s.Notes.MoveBlock(ctx, noteID, blockID, up)
	})
}

func (s *Server) handleNoteBlockDelete(w http.ResponseWriter, r *http.Request) {
	s.blockAction(w, r, func(ctx context.Context, noteID, blockID string) (string, error) {
		return "", s.Notes.DeleteBlock(ctx, noteID, blockID)
	})
}

func (s *Server) handleNoteBlockTask(w http.ResponseWriter, r *http.Request) {
	s.blockAction(w, r, func(ctx context.Context, noteID, blockID string) (string, error) {
		_, err := s.Notes.BlockToTask(ctx, noteID, blockID)
		return "", err
	})
}

func (s *Server) handleNoteBlockArchive(w http.ResponseWriter, r *http.Request) {
	tags := r.FormValue("tags")
	s.blockAction(w, r, func(ctx context.Context, noteID, blockID string) (string, error) {
		_, err := s.Notes.ArchiveBlock(ctx, noteID, blockID, tags)
		return "", err
	})
}

func (s *Server) handleNoteBlockUnarchive(w http.ResponseWriter, r *http.Request) {
	s.blockAction(w, r, func(ctx context.Context, noteID, blockID string) (string, error) {
		_, err := s.Notes.UnarchiveBlock(ctx, noteID, blockID)
		return "", err
	})
}

// handleNoteBlockPreview : l'aperçu Markdown d'une boîte en cours
// d'écriture, rendu par le serveur (déjà échappé) — aucune
// bibliothèque Markdown n'est chargée dans le navigateur.
func (s *Server) handleNoteBlockPreview(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, http.StatusOK, templates.MarkdownPreview(projectinfo.RenderMarkdown(r.FormValue("text"))), "markdown preview")
}

func (s *Server) handleNoteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	if err := s.Notes.DeleteNote(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/notes", http.StatusSeeOther)
}

func (s *Server) handleNoteLinkDoc(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Notes.LinkDoc(r.Context(), id, r.FormValue("doc")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/notes/"+id, http.StatusSeeOther)
}

func (s *Server) handleNoteUnlinkDoc(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Notes.UnlinkDoc(r.Context(), id, chi.URLParam(r, "doc")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/notes/"+id, http.StatusSeeOther)
}

// --- Tâches ---

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	tasks, err := s.Notes.Store.ListTasks(r.Context(), notes.TaskQuery{})
	if err != nil {
		serverError(w, err)
		return
	}
	renderPage(w, r, http.StatusOK, templates.TasksPage(s.taskGroups(r.Context(), tasks, taskContext{})), "tasks")
}

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	if _, err := s.Notes.AddTask(r.Context(), r.FormValue("text"), r.FormValue("note"), r.FormValue("doc")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/tasks"), http.StatusSeeOther)
}

func (s *Server) handleTaskToggle(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	if _, err := s.Notes.ToggleTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/tasks"), http.StatusSeeOther)
}

func (s *Server) handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	t, ok, err := s.Notes.Store.GetTask(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	renderPage(w, r, http.StatusOK, templates.TaskEditPage(s.taskEditView(r.Context(), t, "")), "task")
}

func (s *Server) handleTaskSave(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	t, ok, err := s.Notes.Store.GetTask(ctx, id)
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.Notes.SaveTask(ctx, id, r.FormValue("title"), r.FormValue("due"), r.FormValue("priority"), r.FormValue("note"), r.FormValue("doc")); err != nil {
		// La saisie est réaffichée : un refus ne fait rien perdre.
		t.Title, t.Due, t.Priority = r.FormValue("title"), r.FormValue("due"), notes.ParsePriority(r.FormValue("priority"))
		t.NoteID, t.DocID = r.FormValue("note"), r.FormValue("doc")
		renderPage(w, r, http.StatusUnprocessableEntity, templates.TaskEditPage(s.taskEditView(ctx, t, err.Error())), "task")
		return
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	if err := s.Notes.DeleteTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/tasks"), http.StatusSeeOther)
}

// handleDocumentLinks : notes et tâches liées à un document (fragment
// chargé par la fiche du document).
func (s *Server) handleDocumentLinks(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	ns, err := s.Notes.Store.ListNotes(ctx, notes.NoteQuery{DocID: id})
	if err != nil {
		serverError(w, err)
		return
	}
	tasks, err := s.Notes.Store.ListTasks(ctx, notes.TaskQuery{DocID: id})
	if err != nil {
		serverError(w, err)
		return
	}
	v := templates.DocumentLinksView{DocID: id, Notes: noteCards(ns), Tasks: s.taskGroups(ctx, tasks, taskContext{docID: id})}
	renderPage(w, r, http.StatusOK, templates.DocumentLinks(v), "document links")
}

// --- Vues ---

// taskContext : la page qui affiche les tâches — sur la page d'une note
// (ou d'un document), le lien vers cette même note (ce même document)
// n'est pas répété sur chaque tâche.
type taskContext struct{ noteID, docID, mailID string }

func (s *Server) taskGroups(ctx context.Context, tasks []notes.Task, here taskContext) []templates.TaskGroupView {
	now := s.Notes.Clock()
	var noteIDs, docIDs, mailIDs []string
	for _, t := range tasks {
		if t.NoteID != "" {
			noteIDs = append(noteIDs, t.NoteID)
		}
		if t.DocID != "" {
			docIDs = append(docIDs, t.DocID)
		}
		if t.MailID != "" && t.MailID != here.mailID {
			mailIDs = append(mailIDs, t.MailID)
		}
	}
	noteTitles := s.noteTitles(ctx, noteIDs)
	docNames := s.docNames(ctx, docIDs)
	mailSubjects := s.mailSubjects(ctx, mailIDs)
	var out []templates.TaskGroupView
	for _, g := range notes.GroupTasks(tasks, now) {
		gv := templates.TaskGroupView{Bucket: g.Bucket, Label: g.Bucket.Label()}
		for _, t := range g.Tasks {
			if here.noteID != "" && t.NoteID == here.noteID {
				t.NoteID = ""
			}
			if here.docID != "" && t.DocID == here.docID {
				t.DocID = ""
			}
			if here.mailID != "" && t.MailID == here.mailID {
				t.MailID = ""
			}
			gv.Tasks = append(gv.Tasks, templates.TaskView{
				ID: t.ID, Title: t.Title, Done: t.Done, Due: t.Due,
				DueLabel: notes.DueLabel(t.Due, now), Overdue: notes.BucketOf(t, now) == notes.Overdue,
				Priority: t.Priority,
				NoteID:   t.NoteID, NoteTitle: noteTitles[t.NoteID],
				DocID: t.DocID, DocName: docNames[t.DocID],
				MailID: t.MailID, MailSubject: mailSubjects[t.MailID],
			})
		}
		out = append(out, gv)
	}
	return out
}

func (s *Server) taskEditView(ctx context.Context, t notes.Task, errMsg string) templates.TaskEditView {
	v := templates.TaskEditView{Task: t, Docs: s.recentDocs(ctx), Error: errMsg}
	if all, err := s.Notes.Store.ListNotes(ctx, notes.NoteQuery{}); err == nil {
		for _, n := range all {
			v.Notes = append(v.Notes, templates.DocLink{ID: n.ID, Name: n.Title})
		}
	}
	return v
}

// noteTitles : le titre de chaque note demandée (vide si introuvable).
func (s *Server) noteTitles(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		n, ok, err := s.Notes.Store.GetNote(ctx, id)
		if err == nil && ok {
			out[id] = n.Title
		} else {
			out[id] = "note supprimée"
		}
	}
	return out
}

// docNames : le nom de fichier de chaque document demandé.
func (s *Server) docNames(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		j, ok, err := s.Jobs.Get(ctx, id)
		if err == nil && ok {
			out[id] = j.Filename
		} else {
			out[id] = "document supprimé"
		}
	}
	return out
}

// recentDocs : les documents proposés pour un lien (les plus récents).
func (s *Server) recentDocs(ctx context.Context) []templates.DocLink {
	jobs, err := s.Jobs.List(ctx, webapp.ListQuery{SummaryOnly: true})
	if err != nil {
		return nil
	}
	out := make([]templates.DocLink, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, templates.DocLink{ID: j.ID, Name: j.Filename})
	}
	return out
}

// nonEmpty : id seul, ou rien s'il est vide.
func nonEmpty(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

// blockViews : la colonne de boîtes d'une note. editID : la boîte
// ouverte dans l'éditeur ("" : aucune). showArchived : les boîtes
// archivées sont masquées par défaut, mais toujours comptées.
func (s *Server) blockViews(ctx context.Context, n notes.Note, editID string, showArchived bool) templates.NoteBlocksView {
	v := templates.NoteBlocksView{NoteID: n.ID, EditID: editID, ShowArchived: showArchived}
	for _, b := range notes.BlocksOf(n) {
		if b.Archived {
			v.ArchivedCount++
			if !showArchived {
				continue
			}
		}
		bv := templates.BlockView{
			ID: b.ID, Text: b.Text, HTML: projectinfo.RenderMarkdown(b.Text),
			Tags: b.Tags, Archived: b.Archived, TaskID: b.TaskID, Editing: b.ID == editID,
		}
		if b.Archived && !b.ArchivedAt.IsZero() {
			bv.ArchivedAt = b.ArchivedAt.Local().Format("02/01/2006 15:04")
		}
		if b.TaskID != "" {
			bv.TaskTitle = "tâche supprimée"
			if t, ok, err := s.Notes.Store.GetTask(ctx, b.TaskID); err == nil && ok {
				bv.TaskTitle = t.Title
			}
		}
		v.Blocks = append(v.Blocks, bv)
	}
	return v
}

func noteCards(ns []notes.Note) []templates.NoteCard {
	out := make([]templates.NoteCard, 0, len(ns))
	for _, n := range ns {
		blocks := notes.BlocksOf(n)
		card := templates.NoteCard{
			ID: n.ID, Title: n.Title, Excerpt: excerpt(n.Body, 160), Tags: n.Tags, Pinned: n.Pinned,
			Updated: n.UpdatedAt.Local().Format("02/01/2006 15:04"), Boxes: len(blocks),
		}
		for _, b := range blocks {
			if b.Archived {
				card.Archived++
			}
		}
		out = append(out, card)
	}
	return out
}

// allTags : les tags proposés en filtre — ceux des notes et ceux de
// leurs boîtes (une boîte archivée se retrouve par son tag).
func allTags(ns []notes.Note) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range ns {
		for _, t := range append(append([]string{}, n.Tags...), notes.BlockTags(notes.BlocksOf(n))...) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

// excerpt : le début du texte, sur une ligne, sans marques Markdown.
func excerpt(body string, max int) string {
	s := strings.Join(strings.Fields(strings.NewReplacer("#", "", "*", "", "`", "", "- ", "").Replace(body)), " ")
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
