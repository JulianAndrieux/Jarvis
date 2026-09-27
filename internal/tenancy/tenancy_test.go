package tenancy

import (
	"context"
	"errors"
	"testing"
)

func TestScope_Valid(t *testing.T) {
	if err := (Scope{Env: Local}).Valid(); err != nil {
		t.Errorf("une portée avec un environnement est valide, reçu %v", err)
	}
	err := (Scope{User: "u1"}).Valid()
	if err == nil {
		t.Fatal("une portée sans environnement doit être refusée : c'est elle qui cloisonne les données")
	}
	if !errors.Is(err, ErrNoEnv) {
		t.Errorf("err = %v, veut ErrNoEnv (pour que les stores puissent la reconnaître)", err)
	}
}

// Le travail de fond (pipeline, relève des emails, agents) n'a pas de
// session : c'est ce qui le distingue d'une requête, et ce qui décidera
// plus tard s'il passe par un changeset (jalon 48) ou écrit directement.
func TestScope_Background(t *testing.T) {
	if !System(Local).Background() {
		t.Error("System() est du travail de fond")
	}
	if (Scope{Env: Local, User: "u1", Session: "s1"}).Background() {
		t.Error("une portée avec session n'est pas du travail de fond")
	}
	if err := System(Local).Valid(); err != nil {
		t.Errorf("System() doit être valide, reçu %v", err)
	}
}

func TestScope_Roles(t *testing.T) {
	if !(Scope{Role: RoleOwner}).IsOwner() {
		t.Error("RoleOwner est propriétaire")
	}
	if (Scope{Role: RoleAdmin}).IsOwner() {
		t.Error("un administrateur d'environnement n'est pas propriétaire de l'instance")
	}
	for _, r := range []Role{RoleOwner, RoleAdmin} {
		if !(Scope{Role: r}).CanAdmin() {
			t.Errorf("%s peut administrer son environnement", r)
		}
	}
	if (Scope{Role: RoleMember}).CanAdmin() {
		t.Error("un membre n'administre pas")
	}
}

func TestContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := FromContext(ctx); ok {
		t.Error("un contexte nu ne porte pas de portée")
	}
	want := Scope{Env: "env-2", User: "u7", Session: "s9", Role: RoleMember}
	got, ok := FromContext(WithScope(ctx, want))
	if !ok {
		t.Fatal("la portée posée doit être retrouvée")
	}
	if got != want {
		t.Errorf("portée = %+v, veut %+v", got, want)
	}
}

// Une portée invalide ne doit pas pouvoir être posée dans un contexte :
// sinon l'erreur ne se verrait qu'au premier accès au store, loin de sa
// cause.
func TestWithScope_RefuseUnePorteeInvalide(t *testing.T) {
	ctx := WithScope(context.Background(), Scope{User: "u1"})
	if _, ok := FromContext(ctx); ok {
		t.Error("une portée sans environnement ne doit jamais être visible depuis le contexte")
	}
}
