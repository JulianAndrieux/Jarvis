package notes

import (
	"context"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func journalService(t *testing.T) (*Service, *changes.FakeJournal, context.Context) {
	t.Helper()
	j := changes.NewFakeJournal()
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	svc := &Service{
		Store:   NewFakeStore(),
		Now:     func() time.Time { return at },
		Changes: &changes.Recorder{Journal: j, Now: func() time.Time { return at }},
	}
	ctx := tenancy.WithScope(context.Background(), tenancy.Scope{
		Env: tenancy.Local, User: "u-1", Session: "s-1", Role: tenancy.RoleMember,
	})
	return svc, j, ctx
}

// Une opération par champ réellement modifié : c'est la granularité dont
// le changeset aura besoin, et celle qui évite que deux personnes touchant
// des champs différents se marchent dessus.
func TestSaveNote_JournaliseChaqueChampModifie(t *testing.T) {
	svc, j, ctx := journalService(t)
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveNote(ctx, n.ID, "Courses", "maison, urgent", true); err != nil {
		t.Fatal(err)
	}
	setBody(t, svc, ctx, n.ID, "du lait")

	ops, err := j.List(ctx, changes.Query{Env: tenancy.Local, Kind: changes.KindNote, Target: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]changes.Op{}
	for _, o := range ops {
		got[string(o.Action)+":"+o.Field] = o
	}
	for _, want := range []string{"create:", "set:title", "set:body", "set:tags", "set:pinned"} {
		if _, ok := got[want]; !ok {
			t.Errorf("opération %q absente du journal (reçu %v)", want, keys(got))
		}
	}
	if o := got["set:title"]; o.Before != `"Sans titre"` || o.After != `"Courses"` {
		t.Errorf("titre : before=%q after=%q", o.Before, o.After)
	}
	if o := got["set:title"]; o.User != "u-1" || o.Session != "s-1" {
		t.Errorf("attribution = %s/%s", o.User, o.Session)
	}
	if o := got["set:tags"]; o.After != `["maison","urgent"]` {
		t.Errorf("tags : after=%q", o.After)
	}
}

// Enregistrer sans rien changer ne doit pas remplir le journal.
func TestSaveNote_SansModificationNeJournaliseRien(t *testing.T) {
	svc, j, ctx := journalService(t)
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveNote(ctx, n.ID, "Courses", "maison", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, svc, ctx, n.ID, "du lait")
	before, _ := j.List(ctx, changes.Query{Env: tenancy.Local})
	if _, err := svc.SaveNote(ctx, n.ID, "Courses", "maison", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, svc, ctx, n.ID, "du lait")
	after, _ := j.List(ctx, changes.Query{Env: tenancy.Local})
	if len(after) != len(before) {
		t.Errorf("%d opérations puis %d : un enregistrement sans modification ne doit rien journaliser", len(before), len(after))
	}
}

func TestTachesJournalisees(t *testing.T) {
	svc, j, ctx := journalService(t)
	task, err := svc.AddTask(ctx, "Payer la facture demain !", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ToggleTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	ops, _ := j.List(ctx, changes.Query{Env: tenancy.Local, Kind: changes.KindTask, Target: task.ID})
	var actions []string
	for _, o := range ops {
		actions = append(actions, string(o.Action)+":"+o.Field)
	}
	for _, want := range []string{"create:", "set:done", "delete:"} {
		found := false
		for _, a := range actions {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("opération %q absente (reçu %v)", want, actions)
		}
	}
	// L'étiquette permet de relire le journal après suppression.
	for _, o := range ops {
		if o.Label == "" {
			t.Errorf("opération %s sans étiquette : un identifiant seul ne dit rien", o.Action)
		}
	}
}

// Sans session (travail de fond), rien n'est journalisé.
func TestJournal_IgnoreLeTravailDeFond(t *testing.T) {
	svc, j, _ := journalService(t)
	bg := tenancy.WithScope(context.Background(), tenancy.System(tenancy.Local))
	if _, err := svc.NewNote(bg, ""); err != nil {
		t.Fatal(err)
	}
	if ops, _ := j.List(context.Background(), changes.Query{Env: tenancy.Local}); len(ops) != 0 {
		t.Errorf("%d opérations, veut 0", len(ops))
	}
}

func keys(m map[string]changes.Op) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Le texte d'une boîte est écrit par un humain : il est journalisé, sous
// le champ « body » — le texte de la note, lisible dans l'onglet Activité.
func TestSaveBlock_JournaliseLeTexte(t *testing.T) {
	svc, j, ctx := journalService(t)
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	setBody(t, svc, ctx, n.ID, "du lait")

	ops, err := j.List(ctx, changes.Query{Env: tenancy.Local})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, op := range ops {
		if op.Kind == changes.KindNote && op.Target == n.ID && op.Field == "body" && op.Action == changes.Set {
			found = true
			if op.User != "u-1" || op.Session != "s-1" {
				t.Errorf("attribution = %s/%s", op.User, op.Session)
			}
		}
	}
	if !found {
		t.Errorf("aucune opération « body » dans le journal : %+v", ops)
	}
}
