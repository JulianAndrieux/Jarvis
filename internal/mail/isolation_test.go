package mail

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

// Contrat d'isolation : un email relevé dans un environnement, ses pièces
// jointes comprises, est invisible depuis un autre.
func storeIsolationContract(t *testing.T, root Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	a := root.For(envScope(tenancy.EnvID("A-" + stamp)))
	b := root.For(envScope(tenancy.EnvID("B-" + stamp)))

	id := "mail-" + stamp
	m := Mail{
		ID: id, Account: "julian@example.test", Mailbox: "INBOX", UIDValidity: 7, UID: 42,
		Subject: "Devis " + stamp, Text: "corps secret", Date: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC),
		Attachments: []Attachment{{Index: 0, Filename: "devis.pdf", Size: 3}},
	}
	if created, err := a.Save(ctx, m, map[int][]byte{0: []byte("PDF")}); err != nil || !created {
		t.Fatalf("A.Save = (%v, %v)", created, err)
	}

	if _, ok, err := b.Get(ctx, id); err != nil || ok {
		t.Errorf("B.Get = (ok=%v, err=%v), veut introuvable", ok, err)
	}
	if got, err := b.List(ctx, Query{}); err != nil || len(got) != 0 {
		t.Errorf("B.List = %d (err=%v), veut 0", len(got), err)
	}
	if got, err := b.List(ctx, Query{Search: "secret"}); err != nil || len(got) != 0 {
		t.Errorf("B.List(recherche) = %d (err=%v), veut 0", len(got), err)
	}
	if n, err := b.Count(ctx, Query{}); err != nil || n != 0 {
		t.Errorf("B.Count = %d (err=%v), veut 0", n, err)
	}
	if data, ok, err := b.Attachment(ctx, id, 0); err != nil || ok || data != nil {
		t.Errorf("B.Attachment = (ok=%v, err=%v) : les octets d'un autre environnement ne doivent jamais sortir", ok, err)
	}
	// La relève de B ne doit pas croire avoir déjà relevé les messages de A,
	// sinon elle sauterait des emails.
	if uid, err := b.LastUID(ctx, "julian@example.test", "INBOX", 7); err != nil || uid != 0 {
		t.Errorf("B.LastUID = %d (err=%v), veut 0", uid, err)
	}
	if err := b.SetTriage(ctx, id, Triage{Category: Info}); err == nil {
		t.Error("B.SetTriage a réussi sur un email de A")
	}
	if err := b.SetAttachmentDoc(ctx, id, 0, "doc-pirate"); err == nil {
		t.Error("B.SetAttachmentDoc a réussi sur un email de A")
	}

	got, ok, err := a.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("A.Get = (ok=%v, err=%v)", ok, err)
	}
	if got.Triage.Category != "" {
		t.Errorf("B a trié l'email de A (%q)", got.Triage.Category)
	}
	if got.Env != tenancy.EnvID("A-"+stamp) {
		t.Errorf("Env = %q, veut %q", got.Env, "A-"+stamp)
	}
	if data, ok, err := a.Attachment(ctx, id, 0); err != nil || !ok || string(data) != "PDF" {
		t.Errorf("A.Attachment = (%q, ok=%v, err=%v)", data, ok, err)
	}
	if uid, err := a.LastUID(ctx, "julian@example.test", "INBOX", 7); err != nil || uid != 42 {
		t.Errorf("A.LastUID = %d (err=%v), veut 42", uid, err)
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
