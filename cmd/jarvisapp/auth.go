package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/accounts"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

const (
	// sessionCookie porte le jeton de session (jamais l'identité, jamais
	// un rôle : tout est relu en base à chaque requête, pour qu'un droit
	// retiré s'applique tout de suite).
	sessionCookie = "jarvis_session"
	// oauthCookie porte l'état et le vérificateur PKCE d'une tentative de
	// connexion en cours. Dans un cookie plutôt qu'en mémoire : la
	// tentative survit à un redémarrage, et il n'y a pas d'état de
	// serveur à purger.
	oauthCookie = "jarvis_oauth"
	// oauthAttemptTTL : au-delà, la tentative est périmée.
	oauthAttemptTTL = 10 * time.Minute
)

// isPublicPath : les seules adresses accessibles sans session. Liste
// explicite, verrouillée par TestRoutes_RequireSessionUnlessPublic — une
// route ajoutée demain n'est pas publique par accident.
func isPublicPath(p string) bool {
	switch p {
	case "/login", "/logout", "/health":
		return true
	}
	return strings.HasPrefix(p, "/auth/") || strings.HasPrefix(p, "/static/")
}

// requiresOwner : ce qui n'appartient qu'au propriétaire de l'instance.
// L'espace Admin exécute `go test`, relit le dépôt et déclenche des
// déploiements : un préfixe d'URL n'est pas un contrôle d'accès, c'est le
// rôle qui décide.
func requiresOwner(p string) bool {
	return p == "/admin" || strings.HasPrefix(p, "/admin/")
}

// authEnabled : l'authentification n'est active que si un fournisseur
// d'identité est configuré. Sans configuration, l'application se comporte
// comme avant le jalon 45 : un seul environnement, un seul utilisateur,
// aucun écran de connexion — c'est ce qui permet de continuer à la lancer
// depuis le Dock sans rien préparer.
func (s *Server) authEnabled() bool { return s.Accounts != nil }

// scopeOf rend la portée de la requête, posée par le middleware.
func scopeOf(r *http.Request) tenancy.Scope {
	if sc, ok := tenancy.FromContext(r.Context()); ok {
		return sc
	}
	return tenancy.Scope{}
}

// withScope résout la session et attache sa portée au contexte. C'est le
// seul endroit où une portée entre dans l'application : jamais depuis une
// URL ni un formulaire, sinon changer d'environnement se ferait en
// éditant une adresse.
func (s *Server) withScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			// Mode mono-utilisateur : la portée de l'installation, avec son
			// identité locale. Une session nommée, et non une portée sans
			// session : sans elle, ni le journal ni le changeset ne
			// s'appliqueraient, puisque « sans session » veut dire « travail
			// de fond ».
			next.ServeHTTP(w, r.WithContext(tenancy.WithScope(r.Context(), tenancy.LocalScope())))
			return
		}

		scope, ok := s.resolveSession(r)
		if !ok {
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			redirectToLogin(w, r)
			return
		}
		if requiresOwner(r.URL.Path) && !scope.IsOwner() {
			http.Error(w, "Réservé au propriétaire de cette instance.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(tenancy.WithScope(r.Context(), scope)))
	})
}

func (s *Server) resolveSession(r *http.Request) (tenancy.Scope, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return tenancy.Scope{}, false
	}
	scope, ok, err := s.Accounts.Resolve(r.Context(), c.Value)
	if err != nil {
		log.Printf("jarvisapp: résolution de session : %v", err)
		return tenancy.Scope{}, false
	}
	return scope, ok
}

// redirectToLogin renvoie vers la connexion. HTMX ne suit pas une
// redirection HTTP sur un fragment : il faut l'en-tête HX-Redirect, sinon
// la page afficherait l'écran de connexion à l'intérieur d'un volet.
func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) authRoutes(r chi.Router) {
	r.Get("/login", s.handleLogin)
	r.Get("/auth/google", s.handleAuthStart)
	r.Get("/auth/callback", s.handleAuthCallback)
	r.Get("/auth/local", s.handleAuthLocal)
	r.Post("/logout", s.handleLogout)
	r.Post("/env/switch", s.handleEnvSwitch)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if _, ok := s.resolveSession(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	view := templates.LoginView{
		GoogleReady: s.OAuth.ClientID != "",
		Reason:      loginReason(r.URL.Query().Get("raison")),
	}
	renderPage(w, r, http.StatusOK, templates.LoginPage(view), "login")
}

// loginReason traduit un code en phrase. Le message ne vient jamais de
// l'URL directement : sinon n'importe qui pourrait faire afficher son
// texte sur l'écran de connexion de quelqu'un d'autre.
func loginReason(code string) string {
	switch code {
	case "refuse":
		return "Ce compte Google n'a accès à aucun environnement. Demande une invitation à l'administrateur."
	case "non-verifie":
		return "Cette adresse Google n'est pas vérifiée."
	case "desactive":
		return "Ce compte est désactivé."
	case "echec":
		return "La connexion a échoué. Réessaie."
	case "expire":
		return "La tentative de connexion a expiré. Réessaie."
	case "deconnecte":
		return "Tu es déconnecté."
	}
	return ""
}

func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() || s.OAuth.ClientID == "" {
		http.Error(w, "Connexion Google non configurée.", http.StatusNotFound)
		return
	}
	a, err := s.OAuth.Start()
	if err != nil {
		log.Printf("jarvisapp: début de connexion : %v", err)
		http.Redirect(w, r, "/login?raison=echec", http.StatusSeeOther)
		return
	}
	// L'état et le vérificateur restent côté navigateur, dans un cookie
	// HttpOnly : le code de la page n'y a pas accès.
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Value:    a.State + "|" + a.Verifier,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(oauthAttemptTTL.Seconds()),
	})
	http.Redirect(w, r, a.AuthURL, http.StatusSeeOther)
}

func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.NotFound(w, r)
		return
	}
	state, verifier, ok := readOAuthCookie(r)
	clearCookie(w, r, oauthCookie)
	if !ok {
		http.Redirect(w, r, "/login?raison=expire", http.StatusSeeOther)
		return
	}
	// subtle.ConstantTimeCompare : l'état est un secret de session, on ne
	// laisse pas fuir sa longueur ni son préfixe par le temps de réponse.
	got := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
		http.Redirect(w, r, "/login?raison=echec", http.StatusSeeOther)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login?raison=echec", http.StatusSeeOther)
		return
	}

	profile, err := s.OAuth.Exchange(r.Context(), code, verifier)
	if err != nil {
		log.Printf("jarvisapp: échange du code : %v", err)
		http.Redirect(w, r, "/login?raison=echec", http.StatusSeeOther)
		return
	}
	s.signInAndRedirect(w, r, accounts.Profile{
		Sub: profile.Sub, Email: profile.Email, Name: profile.Name, EmailVerified: profile.EmailVerified,
	})
}

// handleAuthLocal est la porte de secours du propriétaire : une URL à
// jeton unique, écrite dans le journal au démarrage.
//
// Elle existe parce que se connecter par Google demande Internet. Sans
// elle, une panne du fournisseur (ou une machine hors ligne) interdirait
// d'ouvrir une application par ailleurs entièrement locale. Le jeton ne
// vit que quelques minutes, ne sert qu'une fois, et n'est lisible que par
// qui a accès au journal — c'est-à-dire à la machine.
func (s *Server) handleAuthLocal(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.NotFound(w, r)
		return
	}
	token := s.LocalLogin.take(r.URL.Query().Get("token"), time.Now())
	if !token {
		http.Error(w, "Lien de secours invalide ou déjà utilisé.", http.StatusForbidden)
		return
	}
	owner := s.Accounts.OwnerEmail
	if owner == "" {
		http.Error(w, "Aucun propriétaire configuré.", http.StatusForbidden)
		return
	}
	// Le propriétaire est déclaré par la configuration de la machine : son
	// adresse est donc déjà vérifiée au sens où nous l'entendons ici.
	s.signInAndRedirect(w, r, accounts.Profile{Email: owner, Name: "Propriétaire", EmailVerified: true})
}

func (s *Server) signInAndRedirect(w http.ResponseWriter, r *http.Request, p accounts.Profile) {
	token, sess, err := s.Accounts.SignIn(r.Context(), p, r.UserAgent())
	if err != nil {
		switch {
		case errors.Is(err, accounts.ErrNoMembership):
			http.Redirect(w, r, "/login?raison=refuse", http.StatusSeeOther)
		case errors.Is(err, accounts.ErrEmailNotVerified):
			http.Redirect(w, r, "/login?raison=non-verifie", http.StatusSeeOther)
		case errors.Is(err, accounts.ErrDisabled):
			http.Redirect(w, r, "/login?raison=desactive", http.StatusSeeOther)
		default:
			log.Printf("jarvisapp: connexion : %v", err)
			http.Redirect(w, r, "/login?raison=echec", http.StatusSeeOther)
		}
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		Expires:  sess.ExpiresAt,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authEnabled() {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if err := s.Accounts.SignOut(r.Context(), c.Value); err != nil {
				log.Printf("jarvisapp: déconnexion : %v", err)
			}
		}
	}
	clearCookie(w, r, sessionCookie)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login?raison=deconnecte")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login?raison=deconnecte", http.StatusSeeOther)
}

// handleEnvSwitch change l'environnement courant de la session.
func (s *Server) handleEnvSwitch(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	env := tenancy.EnvID(r.FormValue("env"))
	if err := s.Accounts.SwitchEnv(r.Context(), c.Value, env); err != nil {
		// Un environnement dont on n'est pas membre : refus, pas une page
		// d'erreur qui révélerait s'il existe.
		http.Error(w, "Environnement indisponible.", http.StatusForbidden)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func readOAuthCookie(r *http.Request) (state, verifier string, ok bool) {
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		return "", "", false
	}
	state, verifier, found := strings.Cut(c.Value, "|")
	if !found || state == "" || verifier == "" {
		return "", "", false
	}
	return state, verifier, true
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: -1,
	})
}

// localLoginURL : le lien de secours à écrire dans le journal.
func localLoginURL(addr, token string) string {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	return "http://" + host + "/auth/local?token=" + url.QueryEscape(token)
}

// LocalLogin est le jeton de secours du propriétaire : un seul usage, une
// courte durée de vie. Écrit dans le journal au démarrage, il n'est
// lisible que par qui a accès à la machine.
type LocalLogin struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewLocalLogin fabrique un jeton valable pendant ttl.
func NewLocalLogin(token string, now time.Time, ttl time.Duration) *LocalLogin {
	return &LocalLogin{token: token, expires: now.Add(ttl)}
}

// take vérifie le jeton présenté et le consomme. Comparaison à temps
// constant, et un jeton vide ne vaut jamais rien (sinon un lien sans
// paramètre ouvrirait une session dès que le jeton a été consommé).
func (l *LocalLogin) take(presented string, now time.Time) bool {
	if l == nil || presented == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.token == "" || now.After(l.expires) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(l.token)) != 1 {
		return false
	}
	l.token = "" // un seul usage
	return true
}
