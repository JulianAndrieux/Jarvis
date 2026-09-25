package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/agents"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Jalon 33 : deux espaces — l'application (thème clair : Importer,
// Documents, Emails, Notes, Tâches, Tickets) et l'Admin (thème sombre, sous
// /admin : Agents, Architecture, Classes, Modèle, Tests).

func spacesServer(t *testing.T) *Server {
	t.Helper()
	s, _ := spacesServerWithStore(t)
	return s
}

// spacesServerWithStore : le même serveur, plus la fake de jobs (le
// tableau de bord compte des documents).
func spacesServerWithStore(t *testing.T) (*Server, *webapp.FakeStore) {
	t.Helper()
	s, store := newTestServer(t, &blockingRunner{})
	// Modèle de code d'exemple (Classes, Modèle), comme les tests du
	// navigateur de code.
	s.model = newCodeTestServer().model
	s.Tickets = &tickets.Manager{Store: tickets.NewFakeStore()}
	s.Notes = &notes.Service{Store: notes.NewFakeStore()}
	s.Mail = &mail.Service{Store: mail.NewFakeStore()}
	reg, _ := agents.NewRegistry(context.Background(), agents.NewFakeStore(), agents.Defaults(agents.Models{Documents: "m", Tickets: "m"}))
	s.Agents = reg
	return s, store
}

func page(t *testing.T, s *Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	return rec.Body.String()
}

// navLinks : les liens de la barre de navigation principale.
func navLinks(body string) string {
	i := strings.Index(body, `class="app-nav"`)
	j := strings.Index(body[i:], "</nav>")
	return body[i : i+j]
}

func TestSpaces_UserPagesAreLightWithUserNav(t *testing.T) {
	s := spacesServer(t)
	for _, path := range []string{"/", "/import", "/documents", "/emails", "/notes", "/tasks", "/tickets"} {
		body := page(t, s, path)
		if !strings.Contains(body, `class="theme-user"`) || strings.Contains(body, "prefers-color-scheme") {
			t.Errorf("%s: not the light user theme (whatever the system setting)", path)
		}
		nav := navLinks(body)
		for _, want := range []string{`href="/"`, `href="/import"`, `href="/documents"`, `href="/emails"`, `href="/notes"`, `href="/tasks"`, `href="/tickets"`} {
			if !strings.Contains(nav, want) {
				t.Errorf("%s: user nav lacks %s", path, want)
			}
		}
		for _, admin := range []string{"/agents", "/classes", "/model", "/tests", "/architecture"} {
			if strings.Contains(nav, admin) {
				t.Errorf("%s: user nav shows the admin page %s", path, admin)
			}
		}
		if !strings.Contains(body, `href="/admin"`) {
			t.Errorf("%s: no way to the admin space", path)
		}
	}
}

// L'ordre demandé par le ticket "Revoir ordre des sections" : Tableau de
// bord, Notes, Emails, Documents, Tâches, Tickets, Importer.
func TestSpaces_UserNavOrder(t *testing.T) {
	nav := navLinks(page(t, spacesServer(t), "/"))
	// Le tableau de bord est cherché par son libellé : href="/" est un
	// préfixe de tous les autres liens.
	want := []string{">Tableau de bord<", `href="/notes"`, `href="/emails"`, `href="/documents"`, `href="/tasks"`, `href="/tickets"`, `href="/import"`}
	prev := -1
	for _, marker := range want {
		at := strings.Index(nav, marker)
		if at < 0 {
			t.Fatalf("la navigation n'a pas %s : %s", marker, nav)
		}
		if at <= prev {
			t.Errorf("%s arrive trop tôt dans la navigation : %s", marker, nav)
		}
		prev = at
	}
}

func TestSpaces_AdminPagesAreDarkWithAdminNav(t *testing.T) {
	s := spacesServer(t)
	for _, path := range []string{"/admin/agents", "/admin/architecture", "/admin/classes", "/admin/model", "/admin/tests"} {
		body := page(t, s, path)
		if !strings.Contains(body, `class="theme-admin"`) {
			t.Errorf("%s: not the dark admin theme", path)
		}
		nav := navLinks(body)
		for _, want := range []string{`href="/admin/agents"`, `href="/admin/architecture"`, `href="/admin/classes"`, `href="/admin/model"`, `href="/admin/tests"`} {
			if !strings.Contains(nav, want) {
				t.Errorf("%s: admin nav lacks %s", path, want)
			}
		}
		if strings.Contains(nav, `href="/documents"`) || !strings.Contains(body, "← Application") {
			t.Errorf("%s: admin nav mixes user pages, or has no way back", path)
		}
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/architecture" {
		t.Errorf("/admin = %d %q, want the admin home", rec.Code, rec.Header().Get("Location"))
	}
}

// Les anciennes adresses (favoris) mènent à l'Admin, paramètres compris.
func TestSpaces_OldAdminURLsRedirect(t *testing.T) {
	s := spacesServer(t)
	for old, want := range map[string]string{
		"/classes":                     "/admin/classes",
		"/classes/detail?pkg=a&name=B": "/admin/classes/detail?pkg=a&name=B",
		"/model?type=x":                "/admin/model?type=x",
		"/tests":                       "/admin/tests",
		"/architecture":                "/admin/architecture",
		"/agents/extraction":           "/admin/agents/extraction",
	} {
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, old, nil))
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("GET %s = %d %q, want 301 %s", old, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}
