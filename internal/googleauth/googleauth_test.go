package googleauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStart_URLetPKCE(t *testing.T) {
	c := Config{ClientID: "client-1", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8090/auth/callback",
		AuthURL: "https://accounts.example/authorize"}
	a, err := c.Start()
	if err != nil {
		t.Fatalf("Start : %v", err)
	}
	u, err := url.Parse(a.AuthURL)
	if err != nil {
		t.Fatalf("URL invalide : %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":             "client-1",
		"redirect_uri":          "http://127.0.0.1:8090/auth/callback",
		"response_type":         "code",
		"code_challenge_method": "S256",
		"state":                 a.State,
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, veut %q", k, q.Get(k), want)
		}
	}
	if !strings.Contains(q.Get("scope"), "email") {
		t.Errorf("scope = %q, doit demander l'adresse", q.Get("scope"))
	}
	// Le secret du client ne part jamais dans le navigateur.
	if strings.Contains(a.AuthURL, "secret") {
		t.Error("le secret du client ne doit pas apparaître dans l'URL d'autorisation")
	}
	// PKCE : le défi est bien le S256 du vérificateur, qui lui reste ici.
	sum := sha256.Sum256([]byte(a.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if q.Get("code_challenge") != want {
		t.Errorf("code_challenge = %q, veut le S256 du vérificateur (%q)", q.Get("code_challenge"), want)
	}
	if strings.Contains(a.AuthURL, a.Verifier) {
		t.Error("le vérificateur ne doit jamais partir dans l'URL : c'est tout l'intérêt de PKCE")
	}
	if len(a.Verifier) < 43 {
		t.Errorf("vérificateur de %d caractères, minimum 43 (RFC 7636)", len(a.Verifier))
	}
	if len(a.State) < 16 {
		t.Errorf("état de %d caractères : trop court pour être imprévisible", len(a.State))
	}
}

func TestStart_EtatsDistincts(t *testing.T) {
	c := Config{ClientID: "c", AuthURL: "https://accounts.example/authorize"}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		a, err := c.Start()
		if err != nil {
			t.Fatal(err)
		}
		if seen[a.State] || seen[a.Verifier] {
			t.Fatalf("valeur répétée à la %de tentative", i+1)
		}
		seen[a.State], seen[a.Verifier] = true, true
	}
}

// idToken fabrique un jeton d'identité non signé : la signature n'est pas
// vérifiée ici (voir Exchange), puisque le jeton est obtenu par un appel
// direct au fournisseur, en TLS.
func idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(body) + ".signature"
}

// fakeProvider joue le point de terminaison de jetons de Google et
// enregistre ce qu'il a reçu.
func fakeProvider(t *testing.T, claims map[string]any) (*httptest.Server, *url.Values) {
	t.Helper()
	got := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		*got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","id_token":%q}`, idToken(t, claims))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestExchange(t *testing.T) {
	claims := map[string]any{
		"iss": "https://accounts.google.com", "aud": "client-1", "sub": "g-42",
		"email": "julian@example.test", "email_verified": true, "name": "Julian",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	srv, got := fakeProvider(t, claims)
	c := Config{ClientID: "client-1", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8090/auth/callback", TokenURL: srv.URL}

	p, err := c.Exchange(context.Background(), "le-code", "le-verificateur")
	if err != nil {
		t.Fatalf("Exchange : %v", err)
	}
	if p.Sub != "g-42" || p.Email != "julian@example.test" || p.Name != "Julian" || !p.EmailVerified {
		t.Errorf("profil = %+v", p)
	}
	for k, want := range map[string]string{
		"code": "le-code", "code_verifier": "le-verificateur", "grant_type": "authorization_code",
		"client_id": "client-1", "client_secret": "secret", "redirect_uri": "http://127.0.0.1:8090/auth/callback",
	} {
		if got.Get(k) != want {
			t.Errorf("le fournisseur a reçu %s = %q, veut %q", k, got.Get(k), want)
		}
	}
}

func TestExchange_Refus(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"iss": "https://accounts.google.com", "aud": "client-1", "sub": "g-42",
			"email": "julian@example.test", "email_verified": true,
			"exp": time.Now().Add(time.Hour).Unix(),
		}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"autre destinataire", func(c map[string]any) { c["aud"] = "un-autre-client" }, "aud"},
		{"autre émetteur", func(c map[string]any) { c["iss"] = "https://evil.example" }, "iss"},
		{"jeton expiré", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, "expir"},
		{"sans identifiant de compte", func(c map[string]any) { delete(c, "sub") }, "sub"},
		{"sans adresse", func(c map[string]any) { delete(c, "email") }, "email"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := base()
			tc.mutate(claims)
			srv, _ := fakeProvider(t, claims)
			c := Config{ClientID: "client-1", ClientSecret: "s", TokenURL: srv.URL}
			_, err := c.Exchange(context.Background(), "code", "verif")
			if err == nil {
				t.Fatal("un jeton d'identité invalide doit être refusé")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, doit mentionner %q", err, tc.want)
			}
		})
	}
}

func TestExchange_ErreurDuFournisseur(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c := Config{ClientID: "c", TokenURL: srv.URL}
	_, err := c.Exchange(context.Background(), "code", "verif")
	if err == nil {
		t.Fatal("une erreur du fournisseur doit remonter")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %v, doit reprendre la réponse du fournisseur", err)
	}
}

func TestExchange_ReponseIllisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pas du json")
	}))
	defer srv.Close()
	c := Config{ClientID: "c", TokenURL: srv.URL}
	if _, err := c.Exchange(context.Background(), "code", "verif"); err == nil {
		t.Fatal("une réponse illisible doit être une erreur")
	}
}

// Google() remplit les points de terminaison réels : personne ne doit les
// retaper à la main dans le câblage.
func TestGoogle_Endpoints(t *testing.T) {
	c := Google("id", "secret", "http://127.0.0.1:8090/auth/callback")
	if !strings.HasPrefix(c.AuthURL, "https://accounts.google.com/") {
		t.Errorf("AuthURL = %q", c.AuthURL)
	}
	if !strings.HasPrefix(c.TokenURL, "https://oauth2.googleapis.com/") {
		t.Errorf("TokenURL = %q", c.TokenURL)
	}
	if c.ClientID != "id" || c.ClientSecret != "secret" {
		t.Error("identifiants non repris")
	}
}
