package trading

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// storeContract : FakeStore et MongoStore passent le même contrat. stamp
// rend les identifiants uniques (collection de test partagée).
func storeContract(t *testing.T, s Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	at := func(m int) time.Time { return time.Date(2026, 11, 1, 10, m, 0, 0, time.UTC) }
	slug := func(n string) string { return "contract-" + stamp + "-" + n }

	w := Watch{Slug: slug("a"), Question: "A ?", EventTitle: "Événement A", AddedAt: at(0), AlertMove: 0.05}
	if err := s.AddWatch(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetWatch(ctx, w.Slug)
	if err != nil || !ok || got.Question != "A ?" || got.EventTitle != "Événement A" || !got.AddedAt.Equal(w.AddedAt) || got.AlertMove != 0.05 {
		t.Fatalf("GetWatch = %+v, %v, %v", got, ok, err)
	}
	// Suivre deux fois le même marché : refusé, sans rien changer.
	if err := s.AddWatch(ctx, Watch{Slug: w.Slug, Question: "autre"}); !errors.Is(err, ErrAlreadyWatched) {
		t.Errorf("AddWatch(doublon) = %v, veut ErrAlreadyWatched", err)
	}
	if again, _, _ := s.GetWatch(ctx, w.Slug); again.Question != "A ?" {
		t.Errorf("un doublon refusé a modifié le suivi : %+v", again)
	}
	if _, ok, err := s.GetWatch(ctx, slug("absent")); ok || err != nil {
		t.Errorf("GetWatch(absent) = %v, %v", ok, err)
	}

	if err := s.AddWatch(ctx, Watch{Slug: slug("b"), Question: "B ?", AddedAt: at(5)}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListWatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, x := range list {
		if x.Slug == slug("a") || x.Slug == slug("b") {
			mine = append(mine, x.Slug)
		}
	}
	// Le plus récemment suivi d'abord.
	if !reflect.DeepEqual(mine, []string{slug("b"), slug("a")}) {
		t.Errorf("ListWatches = %v", mine)
	}

	if err := s.SetWatchNote(ctx, w.Slug, "j'attends le débat"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetWatch(ctx, w.Slug); got.Note != "j'attends le débat" || got.Question != "A ?" {
		t.Errorf("après SetWatchNote : %+v", got)
	}
	if err := s.SetWatchNote(ctx, slug("absent"), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetWatchNote(absent) = %v, veut ErrNotFound", err)
	}

	if err := s.RemoveWatch(ctx, w.Slug); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetWatch(ctx, w.Slug); ok {
		t.Error("le suivi retiré est toujours là")
	}
	if err := s.RemoveWatch(ctx, w.Slug); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveWatch(absent) = %v, veut ErrNotFound", err)
	}
	_ = s.RemoveWatch(ctx, slug("b"))
}

func TestFakeStore_Contract(t *testing.T) { storeContract(t, NewFakeStore(), "fake") }

func envScope(env tenancy.EnvID) tenancy.Scope {
	return tenancy.Scope{Env: env, User: "u", Session: "s", Role: tenancy.RoleMember}
}

// Contrat d'isolation : ce qu'un environnement suit est invisible et
// intouchable depuis un autre.
func storeIsolationContract(t *testing.T, root Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	a := root.For(envScope(tenancy.EnvID("A-" + stamp)))
	b := root.For(envScope(tenancy.EnvID("B-" + stamp)))
	slug := "iso-" + stamp
	if err := a.AddWatch(ctx, Watch{Slug: slug, Question: "secret"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.GetWatch(ctx, slug); ok || err != nil {
		t.Errorf("B.GetWatch = %v, %v", ok, err)
	}
	if l, err := b.ListWatches(ctx); err != nil || len(l) != 0 {
		t.Errorf("B.ListWatches = %d, %v", len(l), err)
	}
	if err := b.SetWatchNote(ctx, slug, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("B.SetWatchNote = %v", err)
	}
	if err := b.RemoveWatch(ctx, slug); !errors.Is(err, ErrNotFound) {
		t.Errorf("B.RemoveWatch = %v", err)
	}
	// Le même marché peut être suivi par les deux, indépendamment.
	if err := b.AddWatch(ctx, Watch{Slug: slug, Question: "de B"}); err != nil {
		t.Errorf("B.AddWatch(même slug) = %v", err)
	}
	if got, ok, _ := a.GetWatch(ctx, slug); !ok || got.Question != "secret" || got.Note != "" {
		t.Errorf("A après les écritures de B = %+v, %v", got, ok)
	}
	_ = a.RemoveWatch(ctx, slug)
	_ = b.RemoveWatch(ctx, slug)
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
