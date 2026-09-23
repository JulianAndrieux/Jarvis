package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/agents"
)

func newAgentsServer(t *testing.T) *Server {
	t.Helper()
	s, _ := newTestServer(t, &blockingRunner{})
	reg, err := agents.NewRegistry(context.Background(), agents.NewFakeStore(), agents.Defaults(agents.Models{Documents: "qwen3-8b", Tickets: "qwen3-8b"}))
	if err != nil {
		t.Fatal(err)
	}
	s.Agents = reg
	return s
}

func serve(s *Server, method, target string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

// L'onglet Agents montre les quatre agents du système, groupés, avec
// leur modèle et l'état de leur prompt.
func TestAgentsPage_ListsTheFourAgents(t *testing.T) {
	s := newAgentsServer(t)
	rec := serve(s, http.MethodGet, "/admin/agents", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/admin/agents"`, "Classification de document", "Extraction de document", "Analyse de ticket", "Développement",
		`href="/admin/agents/extraction"`, "qwen3-8b", "Prompt par défaut", "propose_plan",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestAgentPage_EditSaveAndReset(t *testing.T) {
	s := newAgentsServer(t)
	page := serve(s, http.MethodGet, "/admin/agents/extraction", nil).Body.String()
	if !strings.Contains(page, `name="prompt"`) || !strings.Contains(page, "{{type_document}}") {
		t.Fatalf("edit page lacks the prompt editor or its placeholder")
	}

	rec := serve(s, http.MethodPost, "/admin/agents/extraction", url.Values{"prompt": {"Extrait tout : {{type_document}}"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/agents/extraction" {
		t.Fatalf("save: status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := s.Agents.Prompt(agents.Extraction); got != "Extrait tout : {{type_document}}" {
		t.Errorf("prompt in force = %q", got)
	}
	page = serve(s, http.MethodGet, "/admin/agents/extraction", nil).Body.String()
	if !strings.Contains(page, "Personnalisé") || !strings.Contains(page, "Historique") {
		t.Errorf("page after save lacks the customised state or history")
	}

	if rec := serve(s, http.MethodPost, "/admin/agents/extraction/reset", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("reset: status %d", rec.Code)
	}
	if a, _ := s.Agents.Get(agents.Extraction); a.Custom {
		t.Error("still customised after reset")
	}
}

// Prompt refusé : la raison est affichée et le texte saisi n'est pas
// perdu ; l'ancien prompt reste en vigueur.
func TestAgentPage_InvalidPromptShowsReasonAndKeepsInput(t *testing.T) {
	s := newAgentsServer(t)
	rec := serve(s, http.MethodPost, "/admin/agents/classification", url.Values{"prompt": {"Choisis un type <b>vite</b>"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "{{types}} manque") || !strings.Contains(body, "Choisis un type &lt;b&gt;vite&lt;/b&gt;") {
		t.Errorf("body lacks the reason or the typed prompt")
	}
	if a, _ := s.Agents.Get(agents.Classification); a.Custom {
		t.Error("invalid prompt applied")
	}
}

func TestAgentPage_UnknownAgentIs404(t *testing.T) {
	s := newAgentsServer(t)
	if rec := serve(s, http.MethodGet, "/admin/agents/inconnu", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
