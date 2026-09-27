package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/internal/accounts"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// newAuthServer : un serveur avec l'authentification active, son
// propriétaire et un membre d'un second environnement.
func newAuthServer(t *testing.T) (*Server, *accounts.Manager, *accounts.FakeStore) {
	t.Helper()
	store := accounts.NewFakeStore()
	mgr := &accounts.Manager{Store: store, OwnerEmail: "julian@example.test"}
	ctx := context.Background()
	if err := mgr.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap : %v", err)
	}
	if err := store.CreateEnv(ctx, accounts.Environment{ID: "cabinet", Name: "Cabinet"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Invite(ctx, "cabinet", "marie@example.test", tenancy.RoleMember); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, &blockingRunner{})
	s.Accounts = mgr
	s.LocalLogin = NewLocalLogin("jeton-de-secours", time.Now(), 10*time.Minute)
	return s, mgr, store
}

func signIn(t *testing.T, mgr *accounts.Manager, email string) string {
	t.Helper()
	token, _, err := mgr.SignIn(context.Background(), accounts.Profile{
		Sub: "g-" + email, Email: email, Name: email, EmailVerified: true,
	}, "test")
	if err != nil {
		t.Fatalf("SignIn(%s) : %v", email, err)
	}
	return token
}

func withSession(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	return r
}

// Norme : aucune route ne sert de données sans session, sauf celles
// déclarées publiques. Le test énumère les routes réellement montées, donc
// une route ajoutée demain est couverte sans qu'on y pense.
func TestRoutes_RequireSessionUnlessPublic(t *testing.T) {
	s, _, _ := newAuthServer(t)
	routes := s.Routes()
	param := regexp.MustCompile(`\{[^}]*\}`)

	checked, public := 0, 0
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := param.ReplaceAllString(route, "x")
		if len(path) > 1 && strings.HasSuffix(path, "*") {
			path = strings.TrimSuffix(path, "*") + "x"
		}
		req := httptest.NewRequest(method, "http://127.0.0.1:8090"+path, nil)
		// Même origine, sinon c'est le contrôle d'origine (jalon 42) qui
		// répondrait, et ce test ne prouverait rien sur l'authentification.
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)

		if isPublicPath(path) {
			public++
			if rec.Code == http.StatusSeeOther && rec.Header().Get("Location") == "/login" {
				t.Errorf("%s %s est publique mais renvoie vers /login", method, route)
			}
			return nil
		}
		checked++
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("%s %s sans session : code %d (Location=%q), veut une redirection vers /login",
				method, route, rec.Code, rec.Header().Get("Location"))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk : %v", err)
	}
	if checked == 0 || public == 0 {
		t.Fatalf("%d routes protégées et %d publiques : le test ne prouve rien", checked, public)
	}
	t.Logf("%d routes protégées, %d publiques", checked, public)
}

// HTMX ne suit pas une redirection HTTP sur un fragment : sans HX-Redirect,
// l'écran de connexion s'afficherait à l'intérieur d'un volet.
func TestSessionExpiree_HTMXRedirige(t *testing.T) {
	s, _, _ := newAuthServer(t)
	req := httptest.NewRequest("GET", "http://127.0.0.1:8090/documents", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, veut 401", rec.Code)
	}
	if rec.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("HX-Redirect = %q, veut /login", rec.Header().Get("HX-Redirect"))
	}
}

// Norme : l'espace Admin exécute `go test`, relit le dépôt et déclenche des
// déploiements. Un préfixe d'URL n'est pas un contrôle d'accès.
func TestAdminRoutes_RequireOwnerRole(t *testing.T) {
	s, mgr, _ := newAuthServer(t)
	member := signIn(t, mgr, "marie@example.test")
	owner := signIn(t, mgr, "julian@example.test")
	routes := s.Routes()
	param := regexp.MustCompile(`\{[^}]*\}`)

	checked := 0
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !requiresOwner(route) {
			return nil
		}
		path := param.ReplaceAllString(route, "x")
		if strings.HasSuffix(path, "*") {
			path = strings.TrimSuffix(path, "*") + "x"
		}
		req := withSession(httptest.NewRequest(method, "http://127.0.0.1:8090"+path, nil), member)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s avec un simple membre : code %d, veut 403", method, route, rec.Code)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk : %v", err)
	}
	if checked == 0 {
		t.Fatal("aucune route d'administration trouvée")
	}
	// Le propriétaire, lui, passe le contrôle. Vérifié sur le middleware
	// seul : passer par le routeur appellerait le vrai handler des classes,
	// qui a besoin d'un modèle de code analysé (et ce test ne parle pas de
	// ça).
	passed := false
	s.withScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed = true })).
		ServeHTTP(httptest.NewRecorder(), withSession(httptest.NewRequest("GET", "/admin/tests", nil), owner))
	if !passed {
		t.Error("le propriétaire ne doit pas être refusé sur /admin")
	}
	t.Logf("%d routes d'administration réservées au propriétaire", checked)
}

// La portée posée par le middleware est celle de la session : c'est elle
// qui décide quel environnement les handlers voient.
func TestMiddleware_PosePorteeDeLaSession(t *testing.T) {
	s, mgr, _ := newAuthServer(t)
	var got tenancy.Scope
	h := s.withScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = scopeOf(r)
	}))

	h.ServeHTTP(httptest.NewRecorder(), withSession(httptest.NewRequest("GET", "/documents", nil), signIn(t, mgr, "marie@example.test")))
	if got.Env != "cabinet" || got.Role != tenancy.RoleMember {
		t.Errorf("portée = %+v, veut membre de cabinet", got)
	}
	h.ServeHTTP(httptest.NewRecorder(), withSession(httptest.NewRequest("GET", "/documents", nil), signIn(t, mgr, "julian@example.test")))
	if got.Env != tenancy.Local || !got.IsOwner() {
		t.Errorf("portée = %+v, veut propriétaire de local", got)
	}
}

// Sans authentification configurée, rien ne change pour l'installation
// existante : aucun écran de connexion, la portée de l'installation.
func TestSansAuthentification_ComportementInchange(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	var got tenancy.Scope
	h := s.withScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = scopeOf(r) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/documents", nil))
	if rec.Code == http.StatusSeeOther {
		t.Error("sans authentification configurée, aucune redirection vers la connexion")
	}
	if got.Env != tenancy.Local || !got.IsOwner() {
		t.Errorf("portée = %+v, veut propriétaire de local", got)
	}
}

func TestLoginPage(t *testing.T) {
	s, _, _ := newAuthServer(t)
	s.OAuth.ClientID = "client-1"
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/login?raison=refuse", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(body, "/auth/google") {
		t.Error("le bouton Google est attendu quand un client est configuré")
	}
	if !strings.Contains(body, "invitation") {
		t.Errorf("la raison du refus doit être expliquée, reçu : %s", body)
	}

	// Sans client OAuth, pas de bouton qui échouerait.
	s.OAuth.ClientID = ""
	rec = httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/login", nil))
	if strings.Contains(rec.Body.String(), "/auth/google") {
		t.Error("aucun bouton Google sans client configuré")
	}
}

// Le message d'erreur affiché ne vient jamais de l'URL : sinon n'importe
// qui pourrait faire afficher son texte sur l'écran de connexion.
func TestLoginPage_NAfficheJamaisLeTexteDeLURL(t *testing.T) {
	s, _, _ := newAuthServer(t)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET",
		"http://127.0.0.1:8090/login?raison=Appelle+le+06+12+34+56+78", nil))
	if strings.Contains(rec.Body.String(), "06 12 34 56 78") {
		t.Error("un texte venu de l'URL s'est affiché sur l'écran de connexion")
	}
}

// Le lien de secours ouvre une session de propriétaire, une seule fois.
func TestAuthLocal_UnSeulUsage(t *testing.T) {
	s, _, _ := newAuthServer(t)
	routes := s.Routes()

	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/auth/local?token=jeton-de-secours", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d (%s), veut une redirection après connexion", rec.Code, rec.Body.String())
	}
	var token string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			token = c.Value
			if !c.HttpOnly {
				t.Error("le cookie de session doit être HttpOnly : le code d'une page n'a rien à y lire")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, veut Lax", c.SameSite)
			}
		}
	}
	if token == "" {
		t.Fatal("aucun cookie de session posé")
	}
	scope, ok, err := s.Accounts.Resolve(context.Background(), token)
	if err != nil || !ok || !scope.IsOwner() {
		t.Errorf("portée = %+v (ok=%v, err=%v), veut propriétaire", scope, ok, err)
	}

	// Rejoué : refusé.
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/auth/local?token=jeton-de-secours", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, veut 403 : un jeton de secours ne sert qu'une fois", rec.Code)
	}
}

func TestAuthLocal_RefuseUnMauvaisJeton(t *testing.T) {
	s, _, _ := newAuthServer(t)
	for _, q := range []string{"", "?token=", "?token=faux"} {
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/auth/local"+q, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("/auth/local%s : code %d, veut 403", q, rec.Code)
		}
	}
}

func TestLocalLogin_Expire(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	l := NewLocalLogin("t", now, 5*time.Minute)
	if l.take("t", now.Add(6*time.Minute)) {
		t.Error("un jeton périmé ne doit plus valoir")
	}
	l = NewLocalLogin("t", now, 5*time.Minute)
	if !l.take("t", now.Add(time.Minute)) {
		t.Error("un jeton valable doit passer")
	}
}

// Déconnexion : la session meurt côté serveur, pas seulement le cookie
// côté navigateur.
func TestLogout(t *testing.T) {
	s, mgr, _ := newAuthServer(t)
	token := signIn(t, mgr, "julian@example.test")
	req := withSession(httptest.NewRequest("POST", "http://127.0.0.1:8090/logout", nil), token)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("code = %d", rec.Code)
	}
	if _, ok, _ := mgr.Resolve(context.Background(), token); ok {
		t.Error("la session doit être supprimée côté serveur")
	}
}

// Changer d'environnement passe par la session. Un environnement dont on
// n'est pas membre est refusé — et le refus ne dit pas s'il existe.
func TestEnvSwitch(t *testing.T) {
	s, mgr, store := newAuthServer(t)
	token := signIn(t, mgr, "julian@example.test")
	post := func(env string) *httptest.ResponseRecorder {
		req := withSession(httptest.NewRequest("POST", "http://127.0.0.1:8090/env/switch?env="+env, nil), token)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("cabinet"); rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, veut 403 : pas membre de cabinet", rec.Code)
	}
	if rec := post("inexistant"); rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, veut 403 : un environnement inconnu et un environnement interdit répondent pareil", rec.Code)
	}
	owner, _, _ := store.UserByEmail(context.Background(), "julian@example.test")
	if err := store.SetMembership(context.Background(), accounts.Membership{Env: "cabinet", User: owner.ID, Role: tenancy.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if rec := post("cabinet"); rec.Code != http.StatusSeeOther {
		t.Errorf("code = %d, veut une redirection", rec.Code)
	}
	scope, _, _ := mgr.Resolve(context.Background(), token)
	if scope.Env != "cabinet" {
		t.Errorf("environnement = %q, veut cabinet", scope.Env)
	}
}
