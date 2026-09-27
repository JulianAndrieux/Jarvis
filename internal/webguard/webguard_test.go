package webguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Le cas qui motive ce paquet (cf. docs/plan-multi-environnement.md §9.1) :
// un formulaire hostile sur un autre site déclenche POST /tickets, la relève
// automatique prend le brouillon et le pilote automatique le fait développer
// puis déployer. Le navigateur, lui, dit toujours d'où vient la requête.
func TestAllow(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		site    string // Sec-Fetch-Site
		origin  string // Origin
		host    string
		tls     bool
		wantOK  bool
		wantWhy string // sous-chaîne attendue de la raison du refus
	}{
		// Les méthodes idempotentes ne changent rien : jamais refusées, d'où
		// qu'elles viennent (les captures de la relecture visuelle et l'essai
		// à blanc d'un déploiement visitent les pages depuis un autre port).
		{name: "GET inter-site", method: "GET", site: "cross-site", host: "127.0.0.1:8090", wantOK: true},
		{name: "HEAD inter-site", method: "HEAD", site: "cross-site", host: "127.0.0.1:8090", wantOK: true},
		{name: "OPTIONS inter-site", method: "OPTIONS", site: "cross-site", host: "127.0.0.1:8090", wantOK: true},

		// Le chemin normal de l'application : HTMX et les formulaires de la page.
		{name: "POST de la page", method: "POST", site: "same-origin", host: "127.0.0.1:8090", wantOK: true},
		{name: "DELETE de la page", method: "DELETE", site: "same-origin", host: "127.0.0.1:8090", wantOK: true},

		// La faille.
		{name: "POST d'un autre site", method: "POST", site: "cross-site", host: "127.0.0.1:8090", wantOK: false, wantWhy: "cross-site"},
		{name: "DELETE d'un autre site", method: "DELETE", site: "cross-site", host: "127.0.0.1:8090", wantOK: false, wantWhy: "cross-site"},
		{name: "PUT d'un autre site", method: "PUT", site: "cross-site", host: "127.0.0.1:8090", wantOK: false, wantWhy: "cross-site"},
		{name: "PATCH d'un autre site", method: "PATCH", site: "cross-site", host: "127.0.0.1:8090", wantOK: false, wantWhy: "cross-site"},

		// Un autre port de la même machine est "same-site", pas "same-origin" :
		// une autre application locale n'a rien à écrire ici.
		{name: "POST d'un autre port local", method: "POST", site: "same-site", host: "127.0.0.1:8090", wantOK: false, wantWhy: "same-site"},
		// "none" : requête sans initiateur (barre d'adresse). Aucun formulaire
		// de l'application n'arrive comme ça.
		{name: "POST sans initiateur", method: "POST", site: "none", host: "127.0.0.1:8090", wantOK: false, wantWhy: "none"},

		// Sans Sec-Fetch-Site (navigateur ancien), Origin fait foi.
		{name: "POST Origin identique", method: "POST", origin: "http://127.0.0.1:8090", host: "127.0.0.1:8090", wantOK: true},
		{name: "POST Origin étranger", method: "POST", origin: "http://evil.example", host: "127.0.0.1:8090", wantOK: false, wantWhy: "evil.example"},
		{name: "POST Origin en TLS", method: "POST", origin: "https://jarvis.example", host: "jarvis.example", tls: true, wantOK: true},
		{name: "POST Origin http alors que TLS", method: "POST", origin: "http://jarvis.example", host: "jarvis.example", tls: true, wantOK: false, wantWhy: "jarvis.example"},

		// Aucun des deux en-têtes : ce n'est pas un navigateur (curl, script,
		// test d'intégration). La menace est une page web dans le navigateur de
		// l'utilisateur ; un programme local a déjà le shell. Choix assumé.
		{name: "POST sans en-tête (curl)", method: "POST", host: "127.0.0.1:8090", wantOK: true},

		// Un site hostile ne peut pas écrire Sec-Fetch-Site (en-tête interdit
		// au code de page) mais contrôle Origin : le premier prime.
		{name: "Sec-Fetch-Site prime sur Origin", method: "POST", site: "cross-site", origin: "http://127.0.0.1:8090", host: "127.0.0.1:8090", wantOK: false, wantWhy: "cross-site"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, why := Allow(c.method, c.site, c.origin, c.host, c.tls)
			if ok != c.wantOK {
				t.Fatalf("Allow(%q, site=%q, origin=%q) = %v (%s), veut %v", c.method, c.site, c.origin, ok, why, c.wantOK)
			}
			if !ok && !strings.Contains(why, c.wantWhy) {
				t.Errorf("raison = %q, doit contenir %q", why, c.wantWhy)
			}
			if !ok && why == "" {
				t.Error("un refus doit toujours dire pourquoi")
			}
		})
	}
}

func TestMiddleware_RefuseSansAppelerLeHandler(t *testing.T) {
	called := false
	var logged string
	h := Guard{Logf: func(f string, a ...any) { logged = f }}.Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest("POST", "http://127.0.0.1:8090/tickets", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Error("le handler ne doit jamais être atteint")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, veut 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "origine") {
		t.Errorf("le corps doit expliquer le refus, reçu %q", rec.Body.String())
	}
	if logged == "" {
		t.Error("un refus doit être journalisé : c'est le signal d'une vraie tentative")
	}
}

func TestMiddleware_LaissepasserLaPage(t *testing.T) {
	called := false
	h := Guard{}.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	req := httptest.NewRequest("POST", "http://127.0.0.1:8090/tickets", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("une requête de la page doit passer")
	}
}

// Logf nil ne doit pas faire paniquer : le middleware est monté très tôt.
func TestMiddleware_SansLogf(t *testing.T) {
	h := Guard{}.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("POST", "http://127.0.0.1:8090/tickets", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, veut 403", rec.Code)
	}
}
