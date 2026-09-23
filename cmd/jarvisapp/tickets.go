package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Outil de tickets (jalons 26-27) : un ticket décrit un changement voulu
// dans l'application ; l'agent local l'analyse et propose un plan.

func (s *Server) ticketRoutes(r chi.Router) {
	r.Get("/tickets", s.handleTickets)
	r.Post("/tickets", s.handleTicketCreate)
	r.Get("/tickets/{id}", s.handleTicket)
	r.Get("/tickets/{id}/panel", s.handleTicketPanel)
	r.Post("/tickets/{id}/analyze", s.ticketAction(func(ctx context.Context, id string, r *http.Request) error {
		return s.Tickets.StartAnalysis(ctx, id)
	}))
	r.Post("/tickets/{id}/revise", s.ticketAction(func(ctx context.Context, id string, r *http.Request) error {
		return s.Tickets.RequestRevision(ctx, id, r.FormValue("feedback"))
	}))
	r.Post("/tickets/{id}/approve", s.ticketAction(func(ctx context.Context, id string, r *http.Request) error {
		return s.Tickets.ApprovePlan(ctx, id)
	}))
	r.Post("/tickets/{id}/cancel", s.ticketAction(func(ctx context.Context, id string, r *http.Request) error {
		return s.Tickets.Cancel(ctx, id)
	}))
	r.Post("/tickets/{id}/comment", s.ticketAction(func(ctx context.Context, id string, r *http.Request) error {
		return s.Tickets.Comment(ctx, id, r.FormValue("text"))
	}))
	r.Delete("/tickets/{id}", s.handleTicketDelete)
}

func (s *Server) ticketsEnabled(w http.ResponseWriter) bool {
	if s.Tickets == nil {
		http.Error(w, "outil de tickets non configuré", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Server) handleTickets(w http.ResponseWriter, r *http.Request) {
	if !s.ticketsEnabled(w) {
		return
	}
	list, err := s.Tickets.List(r.Context(), "")
	if err != nil {
		http.Error(w, "erreur de lecture des tickets : "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.TicketsPage(list).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render tickets: %v\n", err)
	}
}

func (s *Server) handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	if !s.ticketsEnabled(w) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulaire invalide : "+err.Error(), http.StatusBadRequest)
		return
	}
	t, err := s.Tickets.Create(r.Context(), r.FormValue("title"), r.FormValue("need"), r.FormValue("acceptance"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/tickets/"+t.ID, http.StatusSeeOther)
}

func (s *Server) loadTicket(w http.ResponseWriter, r *http.Request) (tickets.Ticket, bool) {
	if !s.ticketsEnabled(w) {
		return tickets.Ticket{}, false
	}
	t, ok, err := s.Tickets.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "erreur de lecture du ticket : "+err.Error(), http.StatusInternalServerError)
		return tickets.Ticket{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return tickets.Ticket{}, false
	}
	return t, true
}

func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTicket(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.TicketPage(t).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render ticket: %v\n", err)
	}
}

func (s *Server) handleTicketPanel(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTicket(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.TicketPanel(t).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render ticket panel: %v\n", err)
	}
}

// ticketAction applique une action puis renvoie le volet à jour (cible
// HTMX #ticket-panel). Une transition refusée est un 409 avec la raison.
func (s *Server) ticketAction(action func(ctx context.Context, id string, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ticketsEnabled(w) {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "formulaire invalide : "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := action(r.Context(), chi.URLParam(r, "id"), r); err != nil {
			status := http.StatusConflict
			if strings.Contains(err.Error(), "introuvable") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		s.handleTicketPanel(w, r)
	}
}

func (s *Server) handleTicketDelete(w http.ResponseWriter, r *http.Request) {
	if !s.ticketsEnabled(w) {
		return
	}
	if err := s.Tickets.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("HX-Redirect", "/tickets")
	w.WriteHeader(http.StatusOK)
}
