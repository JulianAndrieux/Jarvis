package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/projectinfo"
)

// Jalon 57 : l'assistant personnel. La page ne fait qu'ajouter un message
// et afficher le fil ; la réponse se construit en arrière-plan
// (internal/assistant) et continue si la page est fermée.

// AssistantInfo : ce que la page dit de l'assistant en service.
type AssistantInfo struct {
	// Disabled : pourquoi il ne l'est pas ("" : il l'est).
	Disabled string
	Model    string
	// Sources : les sources ouvertes, en clair (« notes », « emails »…).
	Sources []string
}

func (s *Server) assistantRoutes(r chi.Router) {
	r.Get("/assistant", s.handleAssistant)
	r.Post("/assistant", s.handleAssistantNew)
	r.Get("/assistant/{id}", s.handleAssistantConversation)
	r.Get("/assistant/{id}/thread", s.handleAssistantThread)
	r.Post("/assistant/{id}/messages", s.handleAssistantMessage)
	r.Post("/assistant/{id}/actions/{call}/confirm", s.handleAssistantDecide(true))
	r.Post("/assistant/{id}/actions/{call}/refuse", s.handleAssistantDecide(false))
	r.Post("/assistant/{id}/delete", s.handleAssistantDelete)
	r.Post("/assistant/memory/{id}/forget", s.handleAssistantForget)
}

func (s *Server) assistantEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.Assistant == nil {
		http.NotFound(w, r)
		return false
	}
	return true
}

// spaced : un entier avec des espaces de milliers (« 2 000 000 »).
func spaced(n int) string {
	digits := strconv.Itoa(n)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(d)
	}
	return b.String()
}

// assistantView : ce qui entoure le fil — conversations, mémoire,
// consommation. Une source illisible est dite, elle ne casse pas la page.
func (s *Server) assistantView(r *http.Request, activeID string) templates.AssistantView {
	v := templates.AssistantView{Disabled: s.AssistantInfo.Disabled, Model: s.AssistantInfo.Model, Sources: s.AssistantInfo.Sources}
	if s.Assistant == nil {
		if v.Disabled == "" {
			v.Disabled = "Il n'est pas configuré."
		}
		return v
	}
	ctx := r.Context()
	convs, err := s.Assistant.List(ctx)
	if err != nil {
		v.Error = "Conversations illisibles : " + err.Error()
	}
	for _, c := range convs {
		v.Conversations = append(v.Conversations, templates.ConversationRow{
			ID: c.ID, Title: c.Title, When: conversationWhen(c.UpdatedAt, time.Now()),
			Running: c.Status == assistant.StatusRunning, Failed: c.Status == assistant.StatusFailed,
			Waiting: c.Status == assistant.StatusWaiting, Active: c.ID == activeID,
		})
	}
	if facts, err := s.Assistant.Facts(ctx); err == nil {
		for _, f := range facts {
			v.Facts = append(v.Facts, templates.FactRow{ID: f.ID, Text: f.Text})
		}
	}
	if used, limit, err := s.Assistant.UsageToday(ctx); err == nil {
		word := "jetons"
		if used.Total() <= 1 {
			word = "jeton"
		}
		v.Usage = fmt.Sprintf("%s %s aujourd'hui", spaced(used.Total()), word)
		if limit > 0 {
			v.Usage += " sur " + spaced(limit)
		}
	}
	return v
}

// conversationWhen : l'heure pour aujourd'hui, la date sinon.
func conversationWhen(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("02/01")
}

// threadView met le fil en bulles : un message de l'utilisateur, ou une
// réponse de l'assistant avec les outils qu'il a appelés.
func (s *Server) threadView(r *http.Request, c assistant.Conversation) templates.ThreadView {
	t := templates.ThreadView{ID: c.ID, Title: c.Title, Running: c.Status == assistant.StatusRunning}
	// Les actions qui attendent une réponse : ce qu'elles vont faire, dit
	// par l'outil lui-même.
	for _, p := range s.Assistant.Pending(r.Context(), c) {
		t.Pending = append(t.Pending, templates.PendingView{CallID: p.CallID, Summary: p.Summary, Tool: p.Tool, Input: prettyJSON(p.Input)})
	}
	if c.Status == assistant.StatusFailed {
		t.Error = c.Error
	}
	for i := 0; i < len(c.Entries); i++ {
		e := c.Entries[i]
		when := e.At.Local().Format("15:04")
		switch e.Role {
		case assistant.RoleUser:
			t.Messages = append(t.Messages, templates.MessageView{FromUser: true, Text: e.Text, When: when})
		case assistant.RoleAssistant:
			m := templates.MessageView{When: when}
			if strings.TrimSpace(e.Text) != "" {
				m.HTML = projectinfo.RenderMarkdown(e.Text)
			}
			var results []claudeapi.Result
			if i+1 < len(c.Entries) && c.Entries[i+1].Role == assistant.RoleTools {
				results = c.Entries[i+1].Results
				i++
			}
			for _, call := range e.Calls {
				step := templates.StepView{Tool: call.Name, Label: stepLabel(call), Input: prettyJSON(call.Input), Pending: true}
				for _, res := range results {
					if res.CallID == call.ID {
						step.Output, step.IsError, step.Pending = res.Content, res.IsError, false
					}
				}
				m.Steps = append(m.Steps, step)
			}
			if m.HTML == "" && len(m.Steps) == 0 {
				continue
			}
			t.Messages = append(t.Messages, m)
		}
	}
	return t
}

// stepLabel : « search_notes — impôts » — le nom de l'outil et, s'il n'a
// qu'une chose à dire, son argument principal.
func stepLabel(c claudeapi.Call) string {
	var args map[string]any
	if json.Unmarshal(c.Input, &args) != nil {
		return c.Name
	}
	for _, key := range []string{"query", "title", "fact", "id", "filter"} {
		if v, ok := args[key]; ok {
			text := strings.Join(strings.Fields(fmt.Sprint(v)), " ")
			if r := []rune(text); len(r) > 80 {
				text = string(r[:80]) + "…"
			}
			if text != "" {
				return c.Name + " — " + text
			}
		}
	}
	return c.Name
}

func (s *Server) handleAssistant(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, http.StatusOK, templates.AssistantPage(s.assistantView(r, "")), "assistant")
}

func (s *Server) handleAssistantNew(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	text := r.FormValue("text")
	c, err := s.Assistant.Ask(r.Context(), "", text)
	if err != nil {
		v := s.assistantView(r, "")
		v.Error, v.Draft = err.Error(), text
		renderPage(w, r, http.StatusBadRequest, templates.AssistantPage(v), "assistant")
		return
	}
	http.Redirect(w, r, "/assistant/"+c.ID, http.StatusSeeOther)
}

// conversation : la conversation de l'URL, ou 404 — y compris quand elle
// appartient à quelqu'un d'autre.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) (assistant.Conversation, bool) {
	if !s.assistantEnabled(w, r) {
		return assistant.Conversation{}, false
	}
	c, ok, err := s.Assistant.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		serverError(w, err)
		return assistant.Conversation{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return assistant.Conversation{}, false
	}
	return c, true
}

func (s *Server) handleAssistantConversation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.conversation(w, r)
	if !ok {
		return
	}
	v := s.assistantView(r, c.ID)
	t := s.threadView(r, c)
	v.Thread = &t
	renderPage(w, r, http.StatusOK, templates.AssistantPage(v), "assistant conversation")
}

func (s *Server) handleAssistantThread(w http.ResponseWriter, r *http.Request) {
	c, ok := s.conversation(w, r)
	if !ok {
		return
	}
	renderPage(w, r, http.StatusOK, templates.AssistantThread(s.threadView(r, c)), "assistant thread")
}

func (s *Server) handleAssistantMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.conversation(w, r); !ok {
		return
	}
	id, text := chi.URLParam(r, "id"), r.FormValue("text")
	c, err := s.Assistant.Ask(r.Context(), id, text)
	status := http.StatusOK
	var notice string
	switch {
	case errors.Is(err, assistant.ErrBusy):
		status, notice = http.StatusConflict, "Jarvis répond déjà : attends la fin de sa réponse."
	case errors.Is(err, assistant.ErrWaiting):
		status, notice = http.StatusConflict, "Réponds d'abord à la demande de confirmation."
	case errors.Is(err, assistant.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		status, notice = http.StatusBadRequest, err.Error()
	}
	if err != nil {
		// Le fil tel qu'il est, avec la raison et le texte refusé.
		current, ok, getErr := s.Assistant.Get(r.Context(), id)
		if getErr != nil || !ok {
			http.Error(w, notice, status)
			return
		}
		c = current
	}
	t := s.threadView(r, c)
	if err != nil {
		t.Notice, t.Draft = notice, text
	}
	renderPage(w, r, status, templates.AssistantThread(t), "assistant thread")
}

// handleAssistantDecide : la réponse de l'utilisateur à une demande de
// confirmation. Le tour reprend en arrière-plan ; le fil rendu se sonde.
func (s *Server) handleAssistantDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.assistantEnabled(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		c, err := s.Assistant.Decide(r.Context(), id, chi.URLParam(r, "call"), approve)
		switch {
		case errors.Is(err, assistant.ErrNotFound):
			http.NotFound(w, r)
			return
		case errors.Is(err, assistant.ErrNotPending):
			// Déjà répondu (double clic, autre onglet) : le fil tel qu'il est.
			current, ok, getErr := s.Assistant.Get(r.Context(), id)
			if getErr != nil || !ok {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			renderPage(w, r, http.StatusConflict, templates.AssistantThread(s.threadView(r, current)), "assistant thread")
			return
		case err != nil:
			serverError(w, err)
			return
		}
		renderPage(w, r, http.StatusOK, templates.AssistantThread(s.threadView(r, c)), "assistant thread")
	}
}

func (s *Server) handleAssistantDelete(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	if err := s.Assistant.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, assistant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/assistant", http.StatusSeeOther)
}

func (s *Server) handleAssistantForget(w http.ResponseWriter, r *http.Request) {
	if !s.assistantEnabled(w, r) {
		return
	}
	if err := s.Assistant.Forget(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, assistant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/assistant", http.StatusSeeOther)
}

// assistantKey : la clé d'API de l'assistant et d'où elle vient.
type assistantKey struct {
	APIKey string `json:"api_key"`
	// Model : facultatif, l'emporte sur --assistant-model.
	Model string `json:"model"`
	// Source : le fichier, ou la variable d'environnement (pour le journal).
	Source string `json:"-"`
}

// loadAssistantKey lit la clé dans le fichier path, sinon dans
// ANTHROPIC_API_KEY. Ni l'un ni l'autre : clé vide, sans erreur —
// l'assistant est simplement désactivé, et rien ne part vers Anthropic.
// Jamais en option de ligne de commande : elle serait lisible dans ps.
func loadAssistantKey(path string, getenv func(string) string) (assistantKey, error) {
	if path != "" {
		info, err := os.Stat(path)
		switch {
		case err == nil:
			if info.Mode().Perm()&0o077 != 0 {
				return assistantKey{}, fmt.Errorf("%s est lisible par d'autres comptes de cette machine (droits %o) : chmod 600 %s", path, info.Mode().Perm(), path)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return assistantKey{}, err
			}
			var k assistantKey
			if err := json.Unmarshal(raw, &k); err != nil {
				return assistantKey{}, fmt.Errorf("%s illisible : %w", path, err)
			}
			k.APIKey, k.Model, k.Source = strings.TrimSpace(k.APIKey), strings.TrimSpace(k.Model), path
			if k.APIKey == "" {
				return assistantKey{}, fmt.Errorf("%s ne contient pas de api_key", path)
			}
			return k, nil
		case !errors.Is(err, os.ErrNotExist):
			return assistantKey{}, err
		}
	}
	if key := strings.TrimSpace(getenv("ANTHROPIC_API_KEY")); key != "" {
		return assistantKey{APIKey: key, Source: "variable ANTHROPIC_API_KEY"}, nil
	}
	return assistantKey{}, nil
}

// defaultAssistantConfig : ~/.jarvis/assistant.json.
func defaultAssistantConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".jarvis", "assistant.json")
}

// assistantTools : les outils des sources ouvertes (--assistant-sources).
// Les services ne sont touchés qu'à l'appel d'un outil : la liste se
// construit aussi sans eux, pour décrire l'agent.
func assistantTools(sources string, notesSvc *notes.Service, docs assistant.Documents, box assistant.Mailbox, tix assistant.Tickets, a *assistant.Assistant) ([]assistant.Tool, error) {
	var all []assistant.Tool
	all = append(all, assistant.NotesTools(notesSvc)...)
	all = append(all, assistant.TaskTools(notesSvc)...)
	all = append(all, assistant.DocumentTools(docs)...)
	all = append(all, assistant.MailTools(box)...)
	all = append(all, assistant.MemoryTools(a)...)
	all = append(all, assistant.TicketTools(tix)...)
	return assistant.SelectSources(all, sources)
}

// sourceLabels : les sources ouvertes, dites à l'utilisateur (la mémoire
// est celle de l'assistant, pas une donnée de l'utilisateur).
func sourceLabels(sources string) []string {
	labels := map[string]string{assistant.SourceNotes: "notes", assistant.SourceTasks: "tâches", assistant.SourceDocuments: "documents", assistant.SourceEmails: "emails"}
	var out []string
	for _, s := range strings.Split(sources, ",") {
		if l, ok := labels[strings.TrimSpace(s)]; ok {
			out = append(out, l)
		}
	}
	return out
}

// assistantToolNames : les noms des outils des sources ouvertes, pour la
// fiche de l'agent (une liste de sources invalide est signalée plus tard,
// au câblage).
func assistantToolNames(sources string) []string {
	tools, err := assistantTools(sources, nil, nil, nil, nil, nil)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}
