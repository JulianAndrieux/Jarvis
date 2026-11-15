package assistant

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func userScope(env, user string) tenancy.Scope {
	return tenancy.Scope{Env: tenancy.EnvID(env), User: tenancy.UserID(user), Session: "s", Role: tenancy.RoleMember}
}

// storeContract : FakeStore et MongoStore passent le même contrat. stamp
// rend les identifiants uniques (collection de test partagée).
func storeContract(t *testing.T, root Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	s := root.For(userScope("env-"+stamp, "moi"))
	at := func(m int) time.Time { return time.Date(2026, 11, 15, 10, m, 0, 0, time.UTC) }
	id := "c-" + stamp

	if err := s.Create(ctx, Conversation{ID: id, Title: "Factures", CreatedAt: at(0), UpdatedAt: at(0)}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, id)
	if err != nil || !ok || got.Title != "Factures" || got.Env != tenancy.EnvID("env-"+stamp) || got.User != "moi" || got.Running {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := s.Get(ctx, "absent-"+stamp); ok || err != nil {
		t.Errorf("Get(absent) = %v, %v", ok, err)
	}

	// Une question : le message est ajouté et la conversation occupée.
	stale := at(1).Add(-time.Hour)
	if err := s.Begin(ctx, id, Message{Role: RoleUser, Text: "Qu'ai-je à payer ?", At: at(1)}, stale); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, id)
	if len(got.Messages) != 1 || got.Messages[0].Text != "Qu'ai-je à payer ?" || !got.Running || !got.RunStartedAt.Equal(at(1)) || !got.UpdatedAt.Equal(at(1)) {
		t.Fatalf("après Begin = %+v", got)
	}
	// Une seconde question pendant la réponse : refusée, rien n'est ajouté.
	if err := s.Begin(ctx, id, Message{Role: RoleUser, Text: "Et demain ?", At: at(2)}, at(2).Add(-time.Hour)); !errors.Is(err, ErrBusy) {
		t.Errorf("Begin(occupée) = %v, veut ErrBusy", err)
	}
	if got, _, _ := s.Get(ctx, id); len(got.Messages) != 1 {
		t.Errorf("une question refusée a été ajoutée : %+v", got.Messages)
	}

	if err := s.AddStep(ctx, id, Step{Tool: "search_emails", Summary: "Emails à payer", At: at(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddStep(ctx, id, Step{Tool: "create_task", Summary: "Tâche créée", Link: "/tasks", Action: true, At: at(1)}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, id)
	if len(got.Live) != 2 || got.Live[1].Tool != "create_task" || !got.Live[1].Action || got.Live[1].Link != "/tasks" {
		t.Fatalf("étapes en cours = %+v", got.Live)
	}

	reply := Message{Role: RoleAssistant, Text: "Deux factures.", Steps: got.Live, At: at(3), System: "consignes", CostUSD: 0.02, Turns: 3}
	if err := s.Finish(ctx, id, reply); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, id)
	if got.Running || len(got.Live) != 0 || len(got.Messages) != 2 || !got.UpdatedAt.Equal(at(3)) {
		t.Fatalf("après Finish = %+v", got)
	}
	last := got.Messages[1]
	if last.Role != RoleAssistant || last.Text != "Deux factures." || len(last.Steps) != 2 || last.System != "consignes" || last.CostUSD != 0.02 || last.Turns != 3 {
		t.Errorf("réponse = %+v", last)
	}

	// Une réponse restée « en cours » au-delà de la limite (processus
	// disparu) ne bloque pas la question suivante.
	if err := s.Begin(ctx, id, Message{Role: RoleUser, Text: "q2", At: at(4)}, at(3)); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(ctx, id, Message{Role: RoleUser, Text: "q3", At: at(50)}, at(40)); err != nil {
		t.Errorf("Begin après une réponse abandonnée = %v", err)
	}
	if got, _, _ := s.Get(ctx, id); len(got.Messages) != 4 || len(got.Live) != 0 {
		t.Errorf("après reprise = %d messages, %d étapes", len(got.Messages), len(got.Live))
	}

	// Liste : la plus récemment modifiée d'abord, sans les messages.
	id2 := "d-" + stamp
	if err := s.Create(ctx, Conversation{ID: id2, Title: "Ancienne", CreatedAt: at(0), UpdatedAt: at(0)}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != id || list[1].ID != id2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if len(list[0].Messages) != 0 || list[0].Title != "Factures" {
		t.Errorf("la liste porte les messages : %+v", list[0])
	}

	for _, call := range []func() error{
		func() error { return s.Begin(ctx, "absent-"+stamp, Message{At: at(5)}, at(4)) },
		func() error { return s.AddStep(ctx, "absent-"+stamp, Step{}) },
		func() error { return s.Finish(ctx, "absent-"+stamp, Message{At: at(5)}) },
		func() error { return s.Delete(ctx, "absent-"+stamp) },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("sur une conversation absente : %v, veut ErrNotFound", err)
		}
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, id); ok {
		t.Error("la conversation supprimée est toujours là")
	}
	_ = s.Delete(ctx, id2)
}

func TestFakeStore_Contract(t *testing.T) { storeContract(t, NewFakeStore(), "fake") }

// Contrat d'isolation : la conversation d'un user est invisible et
// intouchable depuis un autre user ou un autre environnement.
func storeIsolationContract(t *testing.T, root Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	mine := root.For(userScope("A-"+stamp, "moi"))
	id := "iso-" + stamp
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	if err := mine.Create(ctx, Conversation{ID: id, Title: "privée", UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]Store{
		"autre user":          root.For(userScope("A-"+stamp, "collegue")),
		"autre environnement": root.For(userScope("B-"+stamp, "moi")),
	} {
		if _, ok, err := other.Get(ctx, id); ok || err != nil {
			t.Errorf("%s : Get = %v, %v", name, ok, err)
		}
		if l, err := other.List(ctx); err != nil || len(l) != 0 {
			t.Errorf("%s : List = %d, %v", name, len(l), err)
		}
		if err := other.Begin(ctx, id, Message{Text: "x", At: at}, at.Add(-time.Hour)); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Begin = %v", name, err)
		}
		if err := other.AddStep(ctx, id, Step{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : AddStep = %v", name, err)
		}
		if err := other.Finish(ctx, id, Message{At: at}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Finish = %v", name, err)
		}
		if err := other.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Delete = %v", name, err)
		}
	}
	if got, ok, _ := mine.Get(ctx, id); !ok || len(got.Messages) != 0 || got.Running {
		t.Errorf("ma conversation après les essais des autres = %+v, %v", got, ok)
	}
	_ = mine.Delete(ctx, id)
}

func TestFakeStore_Isolation(t *testing.T) { storeIsolationContract(t, NewFakeStore(), "fake") }

// Norme (cf. jalons 43-44) : aucune méthode du Store ne travaille sans
// environnement.
func TestStore_RefusesEveryMethodWithoutScope(t *testing.T) {
	v := reflect.ValueOf(NewFakeStore().For(tenancy.Scope{}))
	ctx := reflect.ValueOf(context.Background())
	checked := 0
	for i := 0; i < v.NumMethod(); i++ {
		if v.Type().Method(i).Name == "For" {
			continue
		}
		m := v.Method(i)
		args := []reflect.Value{ctx}
		for a := 1; a < m.Type().NumIn(); a++ {
			args = append(args, reflect.Zero(m.Type().In(a)))
		}
		var got error
		for _, out := range m.Call(args) {
			if err, ok := out.Interface().(error); ok && err != nil {
				got = err
			}
		}
		if !errors.Is(got, tenancy.ErrNoEnv) {
			t.Errorf("%s sans portée : err = %v, veut tenancy.ErrNoEnv", v.Type().Method(i).Name, got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("aucune méthode vérifiée")
	}
}

func TestConversation_Active(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	c := Conversation{Running: true, RunStartedAt: now.Add(-2 * time.Minute)}
	if !c.Active(now, 10*time.Minute) {
		t.Error("réponse récente tenue pour abandonnée")
	}
	if c.Active(now, time.Minute) {
		t.Error("réponse abandonnée tenue pour en cours")
	}
	if (Conversation{}).Active(now, time.Minute) {
		t.Error("conversation au repos tenue pour occupée")
	}
}
