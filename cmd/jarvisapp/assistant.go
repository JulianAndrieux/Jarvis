package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/assistant"
)

// Jalon 55 : l'assistant — une conversation où Claude cherche et agit dans
// les données de Jarvis par ses outils (internal/assistant).

func (s *Server) assistantRoutes(r chi.Router) {
	r.Get("/assistant", s.handleAssistant)
	r.Post("/assistant", s.handleAssistantStart)
	r.Get("/assistant/{id}", s.handleAssistantConversation)
	r.Get("/assistant/{id}/thread", s.handleAssistantThread)
	r.Post("/assistant/{id}/messages", s.handleAssistantAsk)
	r.Post("/assistant/{id}/delete", s.handleAssistantDelete)
}

func (s *Server) assistantEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.Assistant == nil {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) assistantView(r *http.Request, errMsg, draft string) (templates.AssistantView, error) {
	list, err := s.Assistant.List(r.Context())
	if err != nil {
		return templates.AssistantView{}, err
	}
	now := time.Now()
	v := templates.AssistantView{Error: errMsg, Draft: draft}
	for _, c := range list {
		v.Conversations = append(v.Conversations, templates.AssistantConvRow{
			ID: c.ID, Title: c.Title, When: c.UpdatedAt.Local().Format("02/01 15:04"),
			Running: c.Active(now, s.Assistant.BusyLimit()),
		})
	}
	return v, nil
}

func (s *Server) handleAssistant(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	v, err := s.assistantView(r, "", "")
	if err != nil {
		serverError(w, err)
		return
	}
	renderPage(w, r, http.StatusOK, templates.AssistantPage(v), "assistant")
}

func (s *Server) handleAssistantStart(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	text := r.FormValue("text")
	conv, err := s.Assistant.Start(r.Context(), text)
	if err != nil {
		// Refusée (vide, trop longue) : la saisie est rendue avec la raison.
		v, lerr := s.assistantView(r, err.Error(), text)
		if lerr != nil {
			serverError(w, lerr)
			return
		}
		renderPage(w, r, http.StatusBadRequest, templates.AssistantPage(v), "assistant")
		return
	}
	http.Redirect(w, r, "/assistant/"+conv.ID, http.StatusSeeOther)
}

// assistantThread charge une conversation ; found=false : la réponse (404
// ou erreur) est déjà écrite.
func (s *Server) assistantThread(w http.ResponseWriter, r *http.Request) (templates.AssistantThread, bool) {
	conv, ok, err := s.Assistant.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		serverError(w, err)
		return templates.AssistantThread{}, false
	}
	if !ok {
		// Introuvable, ou la conversation d'un autre : même réponse.
		http.NotFound(w, r)
		return templates.AssistantThread{}, false
	}
	return threadView(conv, time.Now(), s.Assistant.BusyLimit()), true
}

func stepViews(steps []assistant.Step) []templates.AssistantStep {
	out := make([]templates.AssistantStep, 0, len(steps))
	for _, st := range steps {
		v := templates.AssistantStep{Summary: st.Summary, Action: st.Action, Failed: st.Failed, Error: st.Error}
		// Un lien vient des outils de Jarvis ; par prudence, seul un chemin
		// local est rendu cliquable.
		if localBack(st.Link, "") != "" {
			v.Link = st.Link
		}
		out = append(out, v)
	}
	return out
}

// threadView : la conversation prête à afficher.
func threadView(c assistant.Conversation, now time.Time, busyLimit time.Duration) templates.AssistantThread {
	t := templates.AssistantThread{ID: c.ID, Title: c.Title, Running: c.Active(now, busyLimit)}
	for _, m := range c.Messages {
		v := templates.AssistantMessage{User: m.Role == assistant.RoleUser, Text: m.Text, Error: m.Error,
			Steps: stepViews(m.Steps), When: m.At.Local().Format("02/01 15:04")}
		if m.CostUSD > 0 {
			v.Cost = strings.Replace(fmt.Sprintf("≈ %.2f $", m.CostUSD), ".", ",", 1)
		}
		t.Messages = append(t.Messages, v)
	}
	switch {
	case t.Running:
		t.Live, t.Since = stepViews(c.Live), c.RunStartedAt.Local().Format("15:04:05")
	case c.Running:
		// Marquée en cours mais trop ancienne : le processus qui préparait
		// la réponse a disparu (redémarrage). On le dit, et la saisie
		// redevient possible.
		t.Messages = append(t.Messages, templates.AssistantMessage{
			Error: "interrompue par un redémarrage de Jarvis — repose ta question", Steps: stepViews(c.Live),
			When: c.RunStartedAt.Local().Format("02/01 15:04"),
		})
	}
	return t
}

func (s *Server) handleAssistantConversation(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	if t, ok := s.assistantThread(w, r); ok {
		renderPage(w, r, http.StatusOK, templates.AssistantConversationPage(t), "assistant conversation")
	}
}

func (s *Server) handleAssistantThread(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	if t, ok := s.assistantThread(w, r); ok {
		renderPage(w, r, http.StatusOK, templates.AssistantThreadFragment(t), "assistant thread")
	}
}

func (s *Server) handleAssistantAsk(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	id, text := chi.URLParam(r, "id"), r.FormValue("text")
	err := s.Assistant.Ask(r.Context(), id, text)
	switch {
	case err == nil:
		http.Redirect(w, r, "/assistant/"+id, http.StatusSeeOther)
	case errors.Is(err, assistant.ErrNotFound):
		http.NotFound(w, r)
	default:
		status := http.StatusBadRequest
		if errors.Is(err, assistant.ErrBusy) {
			status = http.StatusConflict
		}
		t, ok := s.assistantThread(w, r)
		if !ok {
			return
		}
		t.Error, t.Draft = err.Error(), text
		renderPage(w, r, status, templates.AssistantConversationPage(t), "assistant conversation")
	}
}

func (s *Server) handleAssistantDelete(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	if err := s.Assistant.Delete(r.Context(), chi.URLParam(r, "id")); err != nil && !errors.Is(err, assistant.ErrNotFound) {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/assistant", http.StatusSeeOther)
}
