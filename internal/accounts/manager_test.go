package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func testManager(t *testing.T) (*Manager, *FakeStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	store := NewFakeStore()
	m := &Manager{
		Store:      store,
		Now:        func() time.Time { return now },
		OwnerEmail: "julian@example.test",
	}
	return m, store, &now
}

// Bootstrap crée l'environnement de l'installation et son propriétaire :
// une application lancée depuis le Dock ne doit pas demander qui elle
// sert.
func TestBootstrap(t *testing.T) {
	m, store, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap : %v", err)
	}
	if _, ok, _ := store.Env(ctx, tenancy.Local); !ok {
		t.Error("l'environnement local doit exister")
	}
	owner, ok, _ := store.UserByEmail(ctx, "julian@example.test")
	if !ok {
		t.Fatal("le propriétaire doit exister")
	}
	ms, _ := store.MembershipsOfUser(ctx, owner.ID)
	if len(ms) != 1 || ms[0].Role != tenancy.RoleOwner || ms[0].Env != tenancy.Local {
		t.Errorf("appartenances = %+v, veut propriétaire de local", ms)
	}
	// Idempotent : relancé à chaque démarrage.
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("second Bootstrap : %v", err)
	}
	if envs, _ := store.ListEnvs(ctx); len(envs) != 1 {
		t.Errorf("%d environnements, veut 1 : Bootstrap doit être idempotent", len(envs))
	}
}

func TestSignIn_Owner(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	token, sess, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", Name: "Julian", EmailVerified: true}, "Chrome")
	if err != nil {
		t.Fatalf("SignIn : %v", err)
	}
	if token == "" {
		t.Fatal("un jeton est attendu")
	}
	if len(token) < 32 {
		t.Errorf("jeton de %d caractères : trop court pour être imprévisible", len(token))
	}
	if sess.TokenHash == token {
		t.Error("le jeton ne doit jamais être stocké tel quel")
	}
	if strings.Contains(sess.TokenHash, token) {
		t.Error("le jeton ne doit pas apparaître dans l'empreinte")
	}
	scope, ok, err := m.Resolve(ctx, token)
	if err != nil || !ok {
		t.Fatalf("Resolve = (ok=%v, err=%v)", ok, err)
	}
	if scope.Env != tenancy.Local || scope.Role != tenancy.RoleOwner {
		t.Errorf("portée = %+v, veut propriétaire de local", scope)
	}
	if scope.Session == "" || scope.User == "" {
		t.Errorf("portée incomplète : %+v", scope)
	}
}

// Un compte Google ne donne rien par lui-même : il faut une appartenance.
func TestSignIn_RefuseSansAppartenance(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.SignIn(ctx, Profile{Sub: "g-2", Email: "inconnu@example.test", EmailVerified: true}, "Chrome")
	if !errors.Is(err, ErrNoMembership) {
		t.Errorf("err = %v, veut ErrNoMembership", err)
	}
}

func TestSignIn_RefuseAdresseNonVerifiee(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: false}, "Chrome")
	if !errors.Is(err, ErrEmailNotVerified) {
		t.Errorf("err = %v, veut ErrEmailNotVerified : une adresse non vérifiée chez Google ne prouve rien", err)
	}
}

// Désactiver un compte lui interdit d'entrer — et une connexion Google ne
// doit pas pouvoir le réactiver, d'où une écriture dédiée plutôt qu'un
// champ d'UpsertUser (trouvé en écrivant ce test : l'upsert de connexion
// écrasait silencieusement l'état).
func TestSignIn_RefuseUnCompteDesactive(t *testing.T) {
	m, store, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	u, _, _ := store.UserByEmail(ctx, "julian@example.test")
	if err := store.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: true}, "Chrome")
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, veut ErrDisabled", err)
	}
}

// Un user invité (appartenance créée avant sa première connexion) entre
// avec le rôle qu'on lui a donné, dans son environnement.
func TestSignIn_UserInvite(t *testing.T) {
	m, store, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnv(ctx, Environment{ID: "cabinet", Name: "Cabinet"}); err != nil {
		t.Fatal(err)
	}
	invited, err := m.Invite(ctx, "cabinet", "marie@example.test", tenancy.RoleAdmin)
	if err != nil {
		t.Fatalf("Invite : %v", err)
	}
	token, _, err := m.SignIn(ctx, Profile{Sub: "g-3", Email: "marie@example.test", Name: "Marie", EmailVerified: true}, "Firefox")
	if err != nil {
		t.Fatalf("SignIn : %v", err)
	}
	scope, ok, _ := m.Resolve(ctx, token)
	if !ok {
		t.Fatal("session introuvable")
	}
	if scope.Env != "cabinet" || scope.Role != tenancy.RoleAdmin {
		t.Errorf("portée = %+v, veut admin de cabinet", scope)
	}
	// L'invitation avant connexion et la connexion doivent désigner le même
	// user : sinon l'appartenance porterait sur un compte fantôme.
	if scope.User != invited.ID {
		t.Errorf("user = %q, veut %q (celui de l'invitation)", scope.User, invited.ID)
	}
}

func TestResolve_JetonInconnuOuExpire(t *testing.T) {
	m, _, now := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.Resolve(ctx, "jeton-inventé"); err != nil || ok {
		t.Errorf("Resolve(inconnu) = (ok=%v, err=%v), veut refus sans erreur", ok, err)
	}
	token, _, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: true}, "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(DefaultSessionTTL + time.Minute)
	if _, ok, err := m.Resolve(ctx, token); err != nil || ok {
		t.Errorf("Resolve(expiré) = (ok=%v, err=%v), veut refus : une session doit expirer", ok, err)
	}
}

// L'accès prolonge la session (expiration glissante) sans écrire à chaque
// requête : une application qui sonde toutes les 2 secondes ne doit pas
// écrire en base 30 fois par minute.
func TestResolve_ProlongeSansEcrireAChaqueFois(t *testing.T) {
	m, store, now := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	token, sess, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: true}, "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	first := sess.ExpiresAt

	*now = now.Add(time.Minute)
	if _, ok, _ := m.Resolve(ctx, token); !ok {
		t.Fatal("session perdue")
	}
	after, _, _ := store.SessionByTokenHash(ctx, sess.TokenHash)
	if !after.ExpiresAt.Equal(first) {
		t.Errorf("expiration réécrite après 1 minute (%v -> %v) : trop d'écritures", first, after.ExpiresAt)
	}

	*now = now.Add(TouchInterval + time.Minute)
	if _, ok, _ := m.Resolve(ctx, token); !ok {
		t.Fatal("session perdue")
	}
	after, _, _ = store.SessionByTokenHash(ctx, sess.TokenHash)
	if !after.ExpiresAt.After(first) {
		t.Errorf("expiration = %v, devait être repoussée après %v d'inactivité", after.ExpiresAt, TouchInterval)
	}
}

func TestSignOut(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	token, _, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: true}, "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SignOut(ctx, token); err != nil {
		t.Fatalf("SignOut : %v", err)
	}
	if _, ok, _ := m.Resolve(ctx, token); ok {
		t.Error("la session doit être morte après déconnexion")
	}
	// Déconnecter deux fois n'est pas une erreur (double clic, onglet resté
	// ouvert).
	if err := m.SignOut(ctx, token); err != nil {
		t.Errorf("second SignOut : %v", err)
	}
}

// Changer d'environnement passe par la session, jamais par l'URL — sinon
// il suffirait d'éditer une adresse pour visiter l'environnement d'un
// autre.
func TestSwitchEnv(t *testing.T) {
	m, store, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnv(ctx, Environment{ID: "cabinet", Name: "Cabinet"}); err != nil {
		t.Fatal(err)
	}
	token, _, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: true}, "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	// Pas membre de cabinet : refusé.
	if err := m.SwitchEnv(ctx, token, "cabinet"); !errors.Is(err, ErrNoMembership) {
		t.Errorf("err = %v, veut ErrNoMembership", err)
	}
	scope, _, _ := m.Resolve(ctx, token)
	if scope.Env != tenancy.Local {
		t.Errorf("environnement = %q : un refus ne doit rien changer", scope.Env)
	}

	owner, _, _ := store.UserByEmail(ctx, "julian@example.test")
	if err := store.SetMembership(ctx, Membership{Env: "cabinet", User: owner.ID, Role: tenancy.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := m.SwitchEnv(ctx, token, "cabinet"); err != nil {
		t.Fatalf("SwitchEnv : %v", err)
	}
	scope, _, _ = m.Resolve(ctx, token)
	if scope.Env != "cabinet" || scope.Role != tenancy.RoleMember {
		t.Errorf("portée = %+v, veut membre de cabinet — le rôle suit l'environnement", scope)
	}
}

// Deux connexions ne doivent jamais produire le même jeton.
func TestSignIn_JetonsDistincts(t *testing.T) {
	m, _, _ := testManager(t)
	ctx := context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		token, _, err := m.SignIn(ctx, Profile{Sub: "g-1", Email: "julian@example.test", EmailVerified: true}, "Chrome")
		if err != nil {
			t.Fatal(err)
		}
		if seen[token] {
			t.Fatalf("jeton répété à la %de connexion", i+1)
		}
		seen[token] = true
	}
}
