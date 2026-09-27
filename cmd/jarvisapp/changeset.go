package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
)

func (s *Server) changesetRoutes(r chi.Router) {
	r.Get("/changes", s.handleChanges)
	r.Post("/changes/commit", s.handleChangesCommit)
	r.Post("/changes/discard", s.handleChangesDiscardAll)
	r.Post("/changes/{id}/discard", s.handleChangesDiscard)
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil || s.Notes.Staged == nil {
		http.NotFound(w, r)
		return
	}
	s.renderChanges(w, r, nil, -1, 0)
}

func (s *Server) handleChangesCommit(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil || s.Notes.Staged == nil {
		http.NotFound(w, r)
		return
	}
	res, err := s.Notes.Commit(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	// Les conflits sont affichés en place, avec les deux valeurs : c'est une
	// décision à prendre, pas une erreur à annoncer.
	byOp := map[string]notes.CommitConflict{}
	for _, c := range res.Conflicts {
		byOp[c.Op.ID] = c
	}
	s.renderChanges(w, r, byOp, res.Applied, len(res.Conflicts))
}

func (s *Server) handleChangesDiscardAll(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil || s.Notes.Staged == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Notes.DiscardAll(r.Context()); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/changes", http.StatusSeeOther)
}

func (s *Server) handleChangesDiscard(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil || s.Notes.Staged == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Notes.Discard(r.Context(), chi.URLParam(r, "id")); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/changes", http.StatusSeeOther)
}

// renderChanges affiche les opérations en attente, en y reportant les
// conflits du commit qui vient d'avoir lieu (committed < 0 : aucun).
func (s *Server) renderChanges(w http.ResponseWriter, r *http.Request, conflicts map[string]notes.CommitConflict, committed, conflicted int) {
	ops, err := s.Notes.Pending(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	view := templates.ChangesView{Committed: committed, Conflicted: conflicted}
	for _, op := range ops {
		e := templates.PendingEntry{
			ID:    op.ID,
			Kind:  string(op.Kind),
			Label: op.Label,
			What:  describeOp(op),
			When:  op.At.Local().Format("02/01 15:04"),
			Href:  entityHref(op),
		}
		if c, ok := conflicts[op.ID]; ok {
			e.Conflict = c.Reason
			e.Current = shortJSON(c.Current)
		}
		view.Entries = append(view.Entries, e)
	}
	renderPage(w, r, http.StatusOK, templates.ChangesPage(view), "changes")
}

// pendingCount : le nombre de modifications en attente, pour le compteur de
// la barre latérale. Une erreur laisse le compteur à zéro plutôt que de
// casser la barre — même principe que ses autres sources.
func (s *Server) pendingCount(r *http.Request) int {
	if s.Notes == nil || s.Notes.Staged == nil {
		return 0
	}
	ops, err := s.Notes.Pending(r.Context())
	if err != nil {
		return 0
	}
	return len(ops)
}
