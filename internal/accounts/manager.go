package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// DefaultSessionTTL : durée de vie d'une session sans activité. Un jeton
// volé cesse de valoir quelque chose, contrairement à une session
// éternelle.
const DefaultSessionTTL = 30 * 24 * time.Hour

// TouchInterval : en dessous de cet intervalle, un accès ne réécrit pas la
// session. L'interface se sonde toutes les 2 secondes pendant un
// traitement ; écrire en base à chaque fois serait absurde.
const TouchInterval = time.Hour

// Erreurs distinctes, pour que l'interface puisse expliquer un refus
// plutôt que dire « connexion impossible ».
var (
	ErrEmailNotVerified = errors.New("accounts: adresse non vérifiée")
	ErrDisabled         = errors.New("accounts: compte désactivé")
	ErrNoMembership     = errors.New("accounts: aucun environnement pour ce compte")
)

// Manager est la logique des comptes : connexion, résolution d'une portée,
// déconnexion, invitation. Il ne connaît ni HTTP ni Google.
type Manager struct {
	Store Store
	// Now : horloge (nil : time.Now), fixée dans les tests.
	Now func() time.Time
	// TTL : 0 -> DefaultSessionTTL.
	TTL time.Duration
	// OwnerEmail est l'adresse du propriétaire de l'instance. Jamais
	// devinée : elle vient de la configuration. C'est le seul compte qui
	// obtient une appartenance sans invitation — sinon personne ne
	// pourrait entrer la première fois.
	OwnerEmail string
	// newToken/newID : injectables pour les tests.
	newToken func() (string, error)
	newID    func() (string, error)
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return DefaultSessionTTL
}

// randomString rend n octets aléatoires en base64url — imprévisible, et
// utilisable tel quel dans un cookie comme dans une URL.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("accounts: aléa: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *Manager) token() (string, error) {
	if m.newToken != nil {
		return m.newToken()
	}
	return randomString(32)
}

func (m *Manager) id() (string, error) {
	if m.newID != nil {
		return m.newID()
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("accounts: aléa: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// RandomToken rend un jeton imprévisible (32 octets), utilisable dans une
// URL comme dans un cookie. Exporté pour le lien de secours du
// propriétaire, qui n'est pas une session mais a les mêmes exigences.
func RandomToken() (string, error) { return randomString(32) }

// HashToken : ce qui est enregistré à la place du jeton. Exporté parce que
// l'interface a besoin de retrouver une session depuis un cookie sans
// jamais confier le jeton au store.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Bootstrap crée l'environnement de cette installation et son
// propriétaire s'ils n'existent pas. Idempotent : lancé à chaque
// démarrage. Sans lui, une application lancée depuis le Dock n'aurait
// personne à servir.
func (m *Manager) Bootstrap(ctx context.Context) error {
	if _, ok, err := m.Store.Env(ctx, tenancy.Local); err != nil {
		return err
	} else if !ok {
		if err := m.Store.CreateEnv(ctx, Environment{ID: tenancy.Local, Name: "Mon environnement", CreatedAt: m.now()}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(m.OwnerEmail) == "" {
		// Pas de propriétaire déclaré : l'environnement existe, mais
		// personne ne peut encore entrer par Google. Dit par l'appelant, pas
		// une erreur ici.
		return nil
	}
	owner, err := m.ensureUser(ctx, Profile{Email: m.OwnerEmail})
	if err != nil {
		return err
	}
	return m.Store.SetMembership(ctx, Membership{Env: tenancy.Local, User: owner.ID, Role: tenancy.RoleOwner})
}

// ensureUser retrouve ou crée l'user d'un profil.
func (m *Manager) ensureUser(ctx context.Context, p Profile) (User, error) {
	id, err := m.id()
	if err != nil {
		return User{}, err
	}
	return m.Store.UpsertUser(ctx, User{
		ID:        tenancy.UserID(id),
		Email:     p.Email,
		GoogleSub: p.Sub,
		Name:      p.Name,
		CreatedAt: m.now(),
	})
}

// Invite donne à une adresse un rôle dans un environnement, avant même sa
// première connexion : le compte est créé vide et sa première connexion
// Google le rattachera (par l'adresse) au lieu d'en créer un second.
func (m *Manager) Invite(ctx context.Context, env tenancy.EnvID, email string, role tenancy.Role) (User, error) {
	if _, ok, err := m.Store.Env(ctx, env); err != nil {
		return User{}, err
	} else if !ok {
		return User{}, fmt.Errorf("accounts: environnement %s inconnu", env)
	}
	u, err := m.ensureUser(ctx, Profile{Email: email})
	if err != nil {
		return User{}, err
	}
	if err := m.Store.SetMembership(ctx, Membership{Env: env, User: u.ID, Role: role}); err != nil {
		return User{}, err
	}
	return u, nil
}

// SignIn ouvre une session pour un profil déjà vérifié par le
// fournisseur d'identité. Rend le jeton à poser dans le cookie — la seule
// fois où il existe en clair.
func (m *Manager) SignIn(ctx context.Context, p Profile, userAgent string) (string, Session, error) {
	if !p.EmailVerified {
		return "", Session{}, fmt.Errorf("%w: %s", ErrEmailNotVerified, p.Email)
	}
	u, err := m.ensureUser(ctx, p)
	if err != nil {
		return "", Session{}, err
	}
	if u.Disabled {
		return "", Session{}, fmt.Errorf("%w: %s", ErrDisabled, p.Email)
	}
	memberships, err := m.Store.MembershipsOfUser(ctx, u.ID)
	if err != nil {
		return "", Session{}, err
	}
	if len(memberships) == 0 {
		// Un compte Google ne donne rien par lui-même : il faut avoir été
		// invité. Sans cette règle, quiconque a un compte Google entrerait.
		return "", Session{}, fmt.Errorf("%w: %s", ErrNoMembership, p.Email)
	}

	token, err := m.token()
	if err != nil {
		return "", Session{}, err
	}
	id, err := m.id()
	if err != nil {
		return "", Session{}, err
	}
	now := m.now()
	sess := Session{
		ID:         tenancy.SessionID(id),
		TokenHash:  HashToken(token),
		User:       u.ID,
		Env:        preferredEnv(memberships),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(m.ttl()),
		UserAgent:  userAgent,
	}
	if err := m.Store.CreateSession(ctx, sess); err != nil {
		return "", Session{}, err
	}
	return token, sess, nil
}

// preferredEnv : l'environnement ouvert par défaut — celui de
// l'installation si le user y appartient, sinon le premier.
func preferredEnv(ms []Membership) tenancy.EnvID {
	for _, m := range ms {
		if m.Env == tenancy.Local {
			return m.Env
		}
	}
	return ms[0].Env
}

// Resolve traduit le jeton d'un cookie en portée. ok=false signifie
// « pas de session valable » (inconnue, expirée, appartenance retirée) —
// jamais une erreur : c'est le cas courant d'un visiteur non connecté.
//
// L'accès prolonge la session, mais au plus une écriture par
// TouchInterval : l'interface se sonde toutes les 2 secondes pendant un
// traitement.
func (m *Manager) Resolve(ctx context.Context, token string) (tenancy.Scope, bool, error) {
	if token == "" {
		return tenancy.Scope{}, false, nil
	}
	sess, ok, err := m.Store.SessionByTokenHash(ctx, HashToken(token))
	if err != nil || !ok {
		return tenancy.Scope{}, false, err
	}
	now := m.now()
	if !sess.ExpiresAt.After(now) {
		return tenancy.Scope{}, false, nil
	}
	u, ok, err := m.Store.UserByID(ctx, sess.User)
	if err != nil {
		return tenancy.Scope{}, false, err
	}
	if !ok || u.Disabled {
		return tenancy.Scope{}, false, nil
	}
	role, ok, err := m.roleIn(ctx, sess.User, sess.Env)
	if err != nil {
		return tenancy.Scope{}, false, err
	}
	if !ok {
		// L'appartenance a été retirée pendant la session : elle cesse de
		// valoir, sans attendre son expiration.
		return tenancy.Scope{}, false, nil
	}
	if now.Sub(sess.LastSeenAt) >= TouchInterval {
		sess.LastSeenAt = now
		sess.ExpiresAt = now.Add(m.ttl())
		if err := m.Store.UpdateSession(ctx, sess); err != nil {
			return tenancy.Scope{}, false, err
		}
	}
	return tenancy.Scope{Env: sess.Env, User: sess.User, Session: sess.ID, Role: role}, true, nil
}

func (m *Manager) roleIn(ctx context.Context, u tenancy.UserID, env tenancy.EnvID) (tenancy.Role, bool, error) {
	ms, err := m.Store.MembershipsOfUser(ctx, u)
	if err != nil {
		return "", false, err
	}
	for _, mem := range ms {
		if mem.Env == env {
			return mem.Role, true, nil
		}
	}
	return "", false, nil
}

// SignOut supprime la session. Un jeton déjà inconnu n'est pas une erreur
// (double clic, onglet resté ouvert).
func (m *Manager) SignOut(ctx context.Context, token string) error {
	sess, ok, err := m.Store.SessionByTokenHash(ctx, HashToken(token))
	if err != nil || !ok {
		return err
	}
	return m.Store.DeleteSession(ctx, sess.ID)
}

// SwitchEnv change l'environnement courant d'une session. Le changement
// passe par la session, jamais par l'URL : sinon éditer une adresse
// suffirait à visiter l'environnement d'un autre.
func (m *Manager) SwitchEnv(ctx context.Context, token string, env tenancy.EnvID) error {
	sess, ok, err := m.Store.SessionByTokenHash(ctx, HashToken(token))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("accounts: session inconnue")
	}
	if _, ok, err := m.roleIn(ctx, sess.User, env); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNoMembership, env)
	}
	sess.Env = env
	sess.LastSeenAt = m.now()
	return m.Store.UpdateSession(ctx, sess)
}

// EnvironmentsOf : les environnements d'un user, pour le sélecteur de la
// navigation.
func (m *Manager) EnvironmentsOf(ctx context.Context, u tenancy.UserID) ([]Environment, error) {
	ms, err := m.Store.MembershipsOfUser(ctx, u)
	if err != nil {
		return nil, err
	}
	var out []Environment
	for _, mem := range ms {
		e, ok, err := m.Store.Env(ctx, mem.Env)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// PurgeExpired retire les sessions expirées. Appelée au démarrage : rien
// ne sert à garder des sessions mortes.
func (m *Manager) PurgeExpired(ctx context.Context) (int64, error) {
	return m.Store.DeleteExpiredSessions(ctx, m.now())
}
