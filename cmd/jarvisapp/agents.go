package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
)

// Onglet Agents : les agents du système et leur prompt, modifiable. Une
// modification s'applique au prochain appel de l'agent (agents.Registry).

func (s *Server) agentRoutes(r chi.Router) {
	r.Get("/agents", s.handleAgents)
	r.Get("/agents/{id}", s.handleAgent)
	r.Post("/agents/{id}", s.handleAgentSave)
	r.Post("/agents/{id}/reset", s.handleAgentReset)
}

func (s *Server) agentsEnabled(w http.ResponseWriter) bool {
	if s.Agents == nil {
		http.Error(w, "agents non configurés", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w) {
		return
	}
	renderAgentPage(w, r, http.StatusOK, templates.AgentsPage(s.Agents.Agents()), "agents")
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w) {
		return
	}
	a, ok := s.Agents.Get(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	renderAgentPage(w, r, http.StatusOK, templates.AgentPage(a, a.Prompt, ""), "agent "+a.ID)
}

func (s *Server) handleAgentSave(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w) {
		return
	}
	id := chi.URLParam(r, "id")
	a, ok := s.Agents.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	prompt := r.FormValue("prompt")
	if err := s.Agents.SetPrompt(r.Context(), id, prompt); err != nil {
		// Le texte saisi est réaffiché : un refus ne doit rien faire perdre.
		renderAgentPage(w, r, http.StatusUnprocessableEntity, templates.AgentPage(a, prompt, err.Error()), "agent "+id)
		return
	}
	http.Redirect(w, r, "/agents/"+id, http.StatusSeeOther)
}

func (s *Server) handleAgentReset(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, ok := s.Agents.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.Agents.Reset(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/agents/"+id, http.StatusSeeOther)
}

// renderAgentPage écrit une page de l'onglet Agents ; what la nomme
// dans le journal en cas d'échec.
func renderAgentPage(w http.ResponseWriter, r *http.Request, status int, c templ.Component, what string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render %s: %v\n", what, err)
	}
}
