// Package googleauth ouvre une session avec un compte Google, par le flux
// « code d'autorisation » d'OpenID Connect, avec PKCE.
//
// Trois choix expliqués, parce qu'ils évitent chacun du code que beaucoup
// écrivent sans en avoir besoin :
//
//  1. Pas de vérification de signature, donc pas de JWKS, pas de RSA à la
//     main, pas de dépendance nouvelle. Le jeton d'identité n'arrive pas
//     par le navigateur : Jarvis l'obtient en appelant lui-même le point
//     de terminaison de jetons, en TLS. OpenID Connect Core §3.1.3.7
//     admet dans ce cas la validation TLS du serveur à la place du
//     contrôle de signature. Les revendications qui comptent (iss, aud,
//     exp, email_verified) sont vérifiées, elles.
//  2. Pas de nonce : il protège le flux implicite, où le jeton passe par
//     le navigateur. Ici, c'est l'état (state) qui lie la redirection à sa
//     tentative, et PKCE qui lie le code à ce client.
//  3. Redirection sur l'adresse de boucle locale. Google refuse http://
//     partout ailleurs, mais l'autorise sur 127.0.0.1 — c'est ce qui
//     permet à une application locale de s'authentifier sans certificat.
//
// Le paquet ne connaît ni cookie ni base : il rend un profil, l'appelant
// en fait une session (voir internal/accounts).
package googleauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GoogleIssuer : l'émetteur attendu dans le jeton d'identité. Google en
// utilise deux formes historiques, les deux légitimes.
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// Config décrit le client OAuth. Les points de terminaison sont
// paramétrables pour que les tests puissent jouer le rôle du fournisseur
// sans réseau.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string

	AuthURL  string
	TokenURL string

	// Issuers : émetteurs acceptés (vide -> ceux de Google).
	Issuers []string
	// HTTP : client utilisé pour l'échange (nil -> un client à délai borné).
	HTTP *http.Client
}

// Google remplit les points de terminaison réels.
func Google(clientID, clientSecret, redirectURL string) Config {
	return Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
	}
}

// Profile est ce que le fournisseur nous apprend, une fois vérifié.
type Profile struct {
	Sub           string
	Email         string
	Name          string
	EmailVerified bool
}

// Attempt est une tentative de connexion en cours : l'URL où envoyer le
// navigateur, et les deux secrets que le serveur garde pour la vérifier au
// retour.
type Attempt struct {
	AuthURL string
	// State lie la redirection reçue à cette tentative-ci.
	State string
	// Verifier ne quitte jamais le serveur (PKCE) : seul son condensé part
	// dans l'URL.
	Verifier string
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("googleauth: aléa: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Start prépare une tentative de connexion.
func (c Config) Start() (Attempt, error) {
	// 32 octets -> 43 caractères, le minimum de la RFC 7636.
	verifier, err := randomString(32)
	if err != nil {
		return Attempt{}, err
	}
	state, err := randomString(16)
	if err != nil {
		return Attempt{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {c.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		// Pour que le choix du compte soit explicite quand plusieurs
		// comptes Google sont connectés dans le navigateur.
		"prompt": {"select_account"},
	}
	sep := "?"
	if strings.Contains(c.AuthURL, "?") {
		sep = "&"
	}
	return Attempt{AuthURL: c.AuthURL + sep + q.Encode(), State: state, Verifier: verifier}, nil
}

func (c Config) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Exchange échange le code reçu par la redirection contre l'identité.
func (c Config) Exchange(ctx context.Context, code, verifier string) (Profile, error) {
	form := url.Values{
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"redirect_uri":  {c.RedirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Profile{}, fmt.Errorf("googleauth: requête de jeton: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("googleauth: échange du code: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Profile{}, fmt.Errorf("googleauth: lecture de la réponse: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// La réponse du fournisseur dit pourquoi (invalid_grant, redirect_uri
		// mismatch...) : la reprendre telle quelle fait gagner du temps.
		return Profile{}, fmt.Errorf("googleauth: échange du code refusé (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return Profile{}, fmt.Errorf("googleauth: réponse illisible: %w", err)
	}
	if tok.IDToken == "" {
		return Profile{}, fmt.Errorf("googleauth: réponse sans id_token")
	}
	return c.profileFromIDToken(tok.IDToken)
}

// profileFromIDToken lit les revendications du jeton et vérifie celles qui
// comptent. La signature n'est pas vérifiée — voir l'en-tête du paquet.
func (c Config) profileFromIDToken(raw string) (Profile, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Profile{}, fmt.Errorf("googleauth: id_token mal formé")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Profile{}, fmt.Errorf("googleauth: id_token illisible: %w", err)
	}
	var claims struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Exp           int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Profile{}, fmt.Errorf("googleauth: revendications illisibles: %w", err)
	}

	issuers := c.Issuers
	if len(issuers) == 0 {
		issuers = googleIssuers
	}
	okIss := false
	for _, want := range issuers {
		if claims.Iss == want {
			okIss = true
			break
		}
	}
	if !okIss {
		return Profile{}, fmt.Errorf("googleauth: iss inattendu (%q)", claims.Iss)
	}
	if claims.Aud != c.ClientID {
		// Un jeton émis pour un autre client ne nous concerne pas : sans ce
		// contrôle, un jeton obtenu ailleurs ouvrirait une session ici.
		return Profile{}, fmt.Errorf("googleauth: aud inattendu (%q)", claims.Aud)
	}
	if claims.Exp == 0 || time.Now().After(time.Unix(claims.Exp, 0)) {
		return Profile{}, fmt.Errorf("googleauth: jeton expiré")
	}
	if claims.Sub == "" {
		return Profile{}, fmt.Errorf("googleauth: jeton sans sub (identifiant de compte)")
	}
	if claims.Email == "" {
		return Profile{}, fmt.Errorf("googleauth: jeton sans email")
	}
	return Profile{Sub: claims.Sub, Email: claims.Email, Name: claims.Name, EmailVerified: claims.EmailVerified}, nil
}
