package webapp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func envScope(env tenancy.EnvID) tenancy.Scope {
	return tenancy.Scope{Env: env, User: "u-" + tenancy.UserID(env), Session: "s-" + tenancy.SessionID(env), Role: tenancy.RoleMember}
}

// Contrat d'isolation : ce que tout Store doit garantir. Un document écrit
// dans un environnement est invisible et intouchable depuis un autre —
// méthode par méthode, pas seulement pour Get.
//
// C'est le garde-fou du cloisonnement : avec une base partagée, un filtre
// oublié dans un seul des accès Mongo suffirait à faire fuir les documents
// d'un environnement dans un autre.
func storeIsolationContract(t *testing.T, root Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	a := root.For(envScope(tenancy.EnvID("A-" + stamp)))
	b := root.For(envScope(tenancy.EnvID("B-" + stamp)))

	id := "job-" + stamp
	if _, err := a.Create(ctx, Job{ID: id, Filename: "facture.pdf", Status: StatusDone, CreatedAt: time.Now(), Content: []byte("%PDF-1.4 A")}); err != nil {
		t.Fatalf("create dans A : %v", err)
	}

	// Lectures aveugles depuis B.
	if _, ok, err := b.Get(ctx, id); err != nil || ok {
		t.Errorf("B.Get = (ok=%v, err=%v), veut introuvable : un identifiant d'un autre environnement doit se comporter comme inexistant (404, jamais 403)", ok, err)
	}
	if jobs, err := b.List(ctx, ListQuery{}); err != nil || len(jobs) != 0 {
		t.Errorf("B.List = %d jobs (err=%v), veut 0", len(jobs), err)
	}
	if jobs, err := b.List(ctx, ListQuery{Search: "facture"}); err != nil || len(jobs) != 0 {
		t.Errorf("B.List(recherche) = %d jobs (err=%v), veut 0 — la recherche ne doit pas traverser les environnements", len(jobs), err)
	}
	if data, ok, err := b.ReadFile(ctx, id, FileOriginal); err != nil || ok || data != nil {
		t.Errorf("B.ReadFile = (ok=%v, err=%v) : les octets d'un autre environnement ne doivent jamais sortir", ok, err)
	}

	// Écritures refusées depuis B.
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"Update", func() error { return b.Update(ctx, Job{ID: id, Status: StatusFailed}) }},
		{"Delete", func() error { return b.Delete(ctx, id) }},
		{"SetTags", func() error { return b.SetTags(ctx, id, []string{"pirate"}) }},
		{"SetComment", func() error { return b.SetComment(ctx, id, "pirate") }},
		{"SetThumbnail", func() error { return b.SetThumbnail(ctx, id, []byte("png")) }},
		{"SetProgress", func() error { return b.SetProgress(ctx, id, nil) }},
		{"WriteFile", func() error { return b.WriteFile(ctx, id, FileOriginal, []byte("pirate")) }},
	} {
		if err := c.call(); err == nil {
			t.Errorf("B.%s a réussi sur un job de A", c.name)
		}
	}

	// A n'a rien perdu.
	job, ok, err := a.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("A.Get = (ok=%v, err=%v)", ok, err)
	}
	if job.Status != StatusDone {
		t.Errorf("statut = %q, veut %q : une écriture refusée depuis B ne doit rien avoir changé", job.Status, StatusDone)
	}
	if len(job.Tags) != 0 || job.Comment != "" {
		t.Errorf("tags=%v comment=%q : B a modifié le job de A", job.Tags, job.Comment)
	}
	if data, ok, err := a.ReadFile(ctx, id, FileOriginal); err != nil || !ok || string(data) != "%PDF-1.4 A" {
		t.Errorf("A.ReadFile = (%q, ok=%v, err=%v)", data, ok, err)
	}

	// Le même identifiant peut exister dans les deux environnements sans
	// collision : les identifiants ne sont pas un espace de noms partagé.
	if _, err := b.Create(ctx, Job{ID: id, Filename: "autre.pdf", Status: StatusPending, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("B.Create avec le même identifiant : %v", err)
	}
	if job, ok, _ := a.Get(ctx, id); !ok || job.Filename != "facture.pdf" {
		t.Errorf("A voit %q après une création de même identifiant dans B", job.Filename)
	}
	if job, ok, _ := b.Get(ctx, id); !ok || job.Filename != "autre.pdf" {
		t.Errorf("B voit %q", job.Filename)
	}
}

func TestFakeStore_IsolationContract(t *testing.T) {
	storeIsolationContract(t, NewFakeStore(), "fake")
}

// Norme : aucune méthode du Store ne travaille sans environnement. Par
// réflexion, donc une méthode ajoutée demain est couverte sans qu'on y
// pense — c'est ce qui empêche d'écrire dans un environnement vide (ou de
// lire tout le monde) par simple oubli de câblage.
func TestStore_RefusesEveryMethodWithoutScope(t *testing.T) {
	unscoped := NewFakeStore().For(tenancy.Scope{})
	v := reflect.ValueOf(unscoped)
	ctx := reflect.ValueOf(context.Background())

	checked := 0
	for i := 0; i < v.NumMethod(); i++ {
		name := v.Type().Method(i).Name
		if name == "For" {
			continue // rend un Store, pas une erreur
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
		t.Fatal("aucune méthode vérifiée : le test ne prouve rien")
	}
	t.Logf("%d méthodes refusent une portée vide", checked)
}
