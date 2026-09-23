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
	search, tag := r.URL.Query().Get("q"), r.URL.Query().Get("tag")
	list, err := s.Notes.Store.ListNotes(r.Context(), notes.NoteQuery{Search: search, Tag: tag})
	if err != nil {
		serverError(w, err)
		return
	}
	all, err := s.Notes.Store.ListNotes(r.Context(), notes.NoteQuery{})
	if err != nil {
		serverError(w, err)
		return
	}
	renderPage(w, r, http.StatusOK, templates.NotesPage(noteCards(list), search, tag, allTags(all)), "notes")
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
	http.Redirect(w, r, "/notes/"+n.ID+"?edit=1", http.StatusSeeOther)
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
		Note:     n,
		BodyHTML: projectinfo.RenderMarkdown(n.Body),
		Edit:     r.URL.Query().Get("edit") != "",
		Tasks:    s.taskGroups(ctx, tasks, taskContext{noteID: n.ID}),
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
	if _, err := s.Notes.SaveNote(r.Context(), id, r.FormValue("title"), r.FormValue("body"), r.FormValue("tags"), r.FormValue("pinned") != ""); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/notes/"+id, http.StatusSeeOther)
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
type taskContext struct{ noteID, docID string }

func (s *Server) taskGroups(ctx context.Context, tasks []notes.Task, here taskContext) []templates.TaskGroupView {
	now := s.Notes.Clock()
	var noteIDs, docIDs []string
	for _, t := range tasks {
		if t.NoteID != "" {
			noteIDs = append(noteIDs, t.NoteID)
		}
		if t.DocID != "" {
			docIDs = append(docIDs, t.DocID)
		}
	}
	noteTitles := s.noteTitles(ctx, noteIDs)
	docNames := s.docNames(ctx, docIDs)
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
			gv.Tasks = append(gv.Tasks, templates.TaskView{
				ID: t.ID, Title: t.Title, Done: t.Done, Due: t.Due,
				DueLabel: notes.DueLabel(t.Due, now), Overdue: notes.BucketOf(t, now) == notes.Overdue,
				Priority: t.Priority,
				NoteID:   t.NoteID, NoteTitle: noteTitles[t.NoteID],
				DocID: t.DocID, DocName: docNames[t.DocID],
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

func noteCards(ns []notes.Note) []templates.NoteCard {
	out := make([]templates.NoteCard, 0, len(ns))
	for _, n := range ns {
		out = append(out, templates.NoteCard{
			ID: n.ID, Title: n.Title, Excerpt: excerpt(n.Body, 160), Tags: n.Tags, Pinned: n.Pinned,
			Updated: n.UpdatedAt.Local().Format("02/01/2006 15:04"),
		})
	}
	return out
}

func allTags(ns []notes.Note) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range ns {
		for _, t := range n.Tags {
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
