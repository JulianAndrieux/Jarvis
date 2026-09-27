package notes

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func envScope(env tenancy.EnvID) tenancy.Scope {
	return tenancy.Scope{Env: env, User: "u", Session: "s", Role: tenancy.RoleMember}
}

// Contrat d'isolation : une note ou une tâche écrite dans un
// environnement est invisible et intouchable depuis un autre, méthode par
// méthode.
func storeIsolationContract(t *testing.T, root Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	a := root.For(envScope(tenancy.EnvID("A-" + stamp)))
	b := root.For(envScope(tenancy.EnvID("B-" + stamp)))

	nID, tID := "note-"+stamp, "task-"+stamp
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	if err := a.CreateNote(ctx, Note{ID: nID, Title: "Secret " + stamp, Body: "corps", Tags: []string{"privé"}, CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatalf("A.CreateNote : %v", err)
	}
	if err := a.CreateTask(ctx, Task{ID: tID, Title: "Tâche " + stamp, CreatedAt: at}); err != nil {
		t.Fatalf("A.CreateTask : %v", err)
	}

	if _, ok, err := b.GetNote(ctx, nID); err != nil || ok {
		t.Errorf("B.GetNote = (ok=%v, err=%v), veut introuvable", ok, err)
	}
	if _, ok, err := b.GetTask(ctx, tID); err != nil || ok {
		t.Errorf("B.GetTask = (ok=%v, err=%v), veut introuvable", ok, err)
	}
	if got, err := b.ListNotes(ctx, NoteQuery{}); err != nil || len(got) != 0 {
		t.Errorf("B.ListNotes = %d (err=%v), veut 0", len(got), err)
	}
	if got, err := b.ListNotes(ctx, NoteQuery{Search: "Secret"}); err != nil || len(got) != 0 {
		t.Errorf("B.ListNotes(recherche) = %d (err=%v), veut 0", len(got), err)
	}
	if got, err := b.ListTasks(ctx, TaskQuery{}); err != nil || len(got) != 0 {
		t.Errorf("B.ListTasks = %d (err=%v), veut 0", len(got), err)
	}
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"UpdateNote", func() error { return b.UpdateNote(ctx, Note{ID: nID, Title: "pirate"}) }},
		{"DeleteNote", func() error { return b.DeleteNote(ctx, nID) }},
		{"UpdateTask", func() error { return b.UpdateTask(ctx, Task{ID: tID, Title: "pirate"}) }},
		{"DeleteTask", func() error { return b.DeleteTask(ctx, tID) }},
	} {
		if err := c.call(); err == nil {
			t.Errorf("B.%s a réussi sur une donnée de A", c.name)
		}
	}

	n, ok, err := a.GetNote(ctx, nID)
	if err != nil || !ok {
		t.Fatalf("A.GetNote = (ok=%v, err=%v)", ok, err)
	}
	if n.Title != "Secret "+stamp {
		t.Errorf("titre = %q : B a modifié la note de A", n.Title)
	}
	if n.Env != tenancy.EnvID("A-"+stamp) {
		t.Errorf("Env = %q, veut %q : le Store doit renseigner l'environnement", n.Env, "A-"+stamp)
	}
	if task, ok, _ := a.GetTask(ctx, tID); !ok || task.Title != "Tâche "+stamp {
		t.Errorf("tâche de A = %+v", task)
	}
}

func TestFakeStore_IsolationContract(t *testing.T) {
	storeIsolationContract(t, NewFakeStore(), "fake")
}

// Norme : aucune méthode du Store ne travaille sans environnement.
func TestStore_RefusesEveryMethodWithoutScope(t *testing.T) {
	v := reflect.ValueOf(NewFakeStore().For(tenancy.Scope{}))
	ctx := reflect.ValueOf(context.Background())
	checked := 0
	for i := 0; i < v.NumMethod(); i++ {
		name := v.Type().Method(i).Name
		if name == "For" {
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
		if got == nil {
			t.Errorf("%s sans portée n'a pas échoué", name)
			continue
		}
		if !errors.Is(got, tenancy.ErrNoEnv) {
			t.Errorf("%s : err = %v, veut envelopper tenancy.ErrNoEnv", name, got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("aucune méthode vérifiée")
	}
	t.Logf("%d méthodes refusent une portée vide", checked)
}
