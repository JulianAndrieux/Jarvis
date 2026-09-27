package accounts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// storeContract : ce que tout Store doit respecter. FakeStore et
// MongoStore passent le même — sans quoi la fake finit par mentir (leçon
// du jalon 23). stamp rend les identifiants uniques (collections de test
// partagées).
func storeContract(t *testing.T, s Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	id := func(n string) string { return stamp + "-" + n }

	// Users : création, retrouvé par identifiant et par adresse (casse
	// indifférente : une adresse n'est pas sensible à la casse).
	u, err := s.UpsertUser(ctx, User{ID: tenancy.UserID(id("u1")), Email: id("Julian") + "@example.test", GoogleSub: id("sub1"), Name: "Julian", CreatedAt: at})
	if err != nil {
		t.Fatalf("UpsertUser : %v", err)
	}
	if u.ID != tenancy.UserID(id("u1")) {
		t.Fatalf("ID = %q", u.ID)
	}
	if got, ok, err := s.UserByID(ctx, u.ID); err != nil || !ok || got.Email != u.Email {
		t.Errorf("UserByID = (%+v, %v, %v)", got, ok, err)
	}
	if got, ok, err := s.UserByEmail(ctx, strings.ToLower(u.Email)); err != nil || !ok || got.ID != u.ID {
		t.Errorf("UserByEmail(minuscules) = (%+v, %v, %v) : une adresse ne dépend pas de la casse", got, ok, err)
	}

	// Upsert d'un profil déjà connu : retrouvé par son compte Google, nom
	// rafraîchi, pas de doublon.
	again, err := s.UpsertUser(ctx, User{ID: tenancy.UserID(id("autre")), Email: id("Julian") + "@example.test", GoogleSub: id("sub1"), Name: "Julian A.", CreatedAt: at})
	if err != nil {
		t.Fatalf("second UpsertUser : %v", err)
	}
	if again.ID != u.ID {
		t.Errorf("ID = %q, veut %q : un second passage ne doit pas créer de doublon", again.ID, u.ID)
	}
	if again.Name != "Julian A." {
		t.Errorf("nom = %q, veut le nom rafraîchi", again.Name)
	}

	// Un compte désactivé le reste après une connexion : UpsertUser ne
	// touche jamais Disabled.
	if err := s.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("SetUserDisabled : %v", err)
	}
	if _, err := s.UpsertUser(ctx, User{ID: tenancy.UserID(id("x")), Email: id("Julian") + "@example.test", GoogleSub: id("sub1"), Name: "Julian"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.UserByID(ctx, u.ID); !got.Disabled {
		t.Error("une connexion a réactivé un compte désactivé")
	}
	if err := s.SetUserDisabled(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}

	// Environnements.
	env := tenancy.EnvID(id("env"))
	if err := s.CreateEnv(ctx, Environment{ID: env, Name: "Env " + stamp, CreatedAt: at}); err != nil {
		t.Fatalf("CreateEnv : %v", err)
	}
	if got, ok, err := s.Env(ctx, env); err != nil || !ok || got.Name != "Env "+stamp {
		t.Errorf("Env = (%+v, %v, %v)", got, ok, err)
	}
	if _, ok, _ := s.Env(ctx, tenancy.EnvID(id("inconnu"))); ok {
		t.Error("un environnement inconnu ne doit pas être trouvé")
	}

	// Appartenances : un seul rôle par couple, remplaçable.
	if err := s.SetMembership(ctx, Membership{Env: env, User: u.ID, Role: tenancy.RoleMember}); err != nil {
		t.Fatalf("SetMembership : %v", err)
	}
	if err := s.SetMembership(ctx, Membership{Env: env, User: u.ID, Role: tenancy.RoleAdmin}); err != nil {
		t.Fatalf("SetMembership (remplacement) : %v", err)
	}
	ms, err := s.MembershipsOfUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("MembershipsOfUser : %v", err)
	}
	var found int
	for _, m := range ms {
		if m.Env == env {
			found++
			if m.Role != tenancy.RoleAdmin {
				t.Errorf("rôle = %q, veut admin (le remplacement doit être en place)", m.Role)
			}
		}
	}
	if found != 1 {
		t.Errorf("%d appartenances pour cet environnement, veut 1 : un couple ne porte qu'un rôle", found)
	}
	if got, err := s.MembershipsOfEnv(ctx, env); err != nil || len(got) != 1 {
		t.Errorf("MembershipsOfEnv = %d (err=%v), veut 1", len(got), err)
	}

	// Sessions : retrouvée par l'empreinte du jeton, mise à jour ciblée,
	// suppression, purge.
	sess := Session{
		ID: tenancy.SessionID(id("s1")), TokenHash: HashToken(id("jeton")), User: u.ID, Env: env,
		CreatedAt: at, LastSeenAt: at, ExpiresAt: at.Add(time.Hour), UserAgent: "Chrome",
	}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession : %v", err)
	}
	got, ok, err := s.SessionByTokenHash(ctx, sess.TokenHash)
	if err != nil || !ok || got.User != u.ID || got.Env != env {
		t.Fatalf("SessionByTokenHash = (%+v, %v, %v)", got, ok, err)
	}
	if _, ok, _ := s.SessionByTokenHash(ctx, HashToken(id("autre-jeton"))); ok {
		t.Error("un jeton inconnu ne doit pas ouvrir de session")
	}

	moved := got
	moved.Env = tenancy.EnvID(id("ailleurs"))
	moved.LastSeenAt = at.Add(time.Minute)
	moved.ExpiresAt = at.Add(2 * time.Hour)
	moved.TokenHash = HashToken(id("jeton-pirate"))
	moved.User = tenancy.UserID(id("pirate"))
	if err := s.UpdateSession(ctx, moved); err != nil {
		t.Fatalf("UpdateSession : %v", err)
	}
	after, ok, _ := s.SessionByTokenHash(ctx, sess.TokenHash)
	if !ok {
		t.Fatal("la session doit rester trouvable par son jeton d'origine : UpdateSession ne réécrit pas le jeton")
	}
	if after.User != u.ID {
		t.Errorf("user = %q : UpdateSession ne doit pas réécrire le user", after.User)
	}
	if after.Env != tenancy.EnvID(id("ailleurs")) || !after.ExpiresAt.Equal(at.Add(2*time.Hour)) {
		t.Errorf("session = %+v : environnement et expiration doivent être à jour", after)
	}

	if _, err := s.DeleteExpiredSessions(ctx, at); err != nil {
		t.Fatalf("DeleteExpiredSessions : %v", err)
	}
	if _, ok, _ := s.SessionByTokenHash(ctx, sess.TokenHash); !ok {
		t.Error("une session encore valable ne doit pas être purgée")
	}
	if _, err := s.DeleteExpiredSessions(ctx, at.Add(3*time.Hour)); err != nil {
		t.Fatalf("DeleteExpiredSessions : %v", err)
	}
	if _, ok, _ := s.SessionByTokenHash(ctx, sess.TokenHash); ok {
		t.Error("une session expirée doit être purgée")
	}

	if err := s.RemoveMembership(ctx, env, u.ID); err != nil {
		t.Fatalf("RemoveMembership : %v", err)
	}
	if err := s.RemoveMembership(ctx, env, u.ID); err == nil {
		t.Error("retirer une appartenance inexistante doit être une erreur, jamais un succès silencieux")
	}
}

func TestFakeStore_Contract(t *testing.T) {
	storeContract(t, NewFakeStore(), "fake")
}
