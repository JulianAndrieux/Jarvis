package mail

import (
	"context"
	"strings"
	"testing"
	"time"
)

// storeContract : ce que tout Store doit respecter — FakeStore (tests
// unitaires) et MongoStore (intégration, Atlas) passent le même contrat.
// stamp rend identifiants et recherches uniques (collections de test
// partagées).
func storeContract(t *testing.T, s Store, stamp string) {
	ctx := context.Background()
	at := func(h int) time.Time { return time.Date(2026, 9, 20+h, 9, 0, 0, 0, time.UTC) }
	account := "moi-" + stamp + "@gmail.com"
	mk := func(uid uint32, subject string, day int) Mail {
		return Mail{
			ID: stamp + "-" + subject, Account: account, Mailbox: "INBOX", UIDValidity: 7, UID: uid,
			From: Address{Name: "Alice", Email: "alice@example.com"}, To: []Address{{Email: account}},
			Subject: subject + " " + stamp, Date: at(day), Text: "Corps de " + subject,
			FetchedAt: at(day),
		}
	}

	// Enregistrement, pièce jointe, et second enregistrement sans effet.
	facture := mk(40, "facture", 1)
	facture.Attachments = []Attachment{{Index: 0, Filename: "f.pdf", ContentType: "application/pdf", Size: 4, Stored: true}}
	created, err := s.Save(ctx, facture, map[int][]byte{0: []byte("%PDF")})
	if err != nil || !created {
		t.Fatalf("Save = %v, %v", created, err)
	}
	again := facture
	again.Subject = "remplacé"
	if created, err := s.Save(ctx, again, nil); err != nil || created {
		t.Errorf("Save(existing) = %v, %v, want not created", created, err)
	}
	got, ok, err := s.Get(ctx, facture.ID)
	if err != nil || !ok || got.Subject != facture.Subject || got.From.Email != "alice@example.com" || len(got.Attachments) != 1 || !got.Date.Equal(facture.Date) || got.UID != 40 {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}
	data, ok, err := s.Attachment(ctx, facture.ID, 0)
	if err != nil || !ok || string(data) != "%PDF" {
		t.Errorf("Attachment = %q, %v, %v", data, ok, err)
	}
	if _, ok, err := s.Attachment(ctx, facture.ID, 3); ok || err != nil {
		t.Errorf("Attachment(unknown) = %v, %v", ok, err)
	}
	if _, ok, err := s.Get(ctx, stamp+"-absent"); ok || err != nil {
		t.Errorf("Get(unknown) = %v, %v", ok, err)
	}

	// Dernier UID vu, par compte, boîte et UIDVALIDITY.
	for _, m := range []Mail{mk(45, "réunion", 3), mk(42, "promo", 2)} {
		if _, err := s.Save(ctx, m, nil); err != nil {
			t.Fatal(err)
		}
	}
	if uid, err := s.LastUID(ctx, account, "INBOX", 7); err != nil || uid != 45 {
		t.Errorf("LastUID = %d, %v, want 45", uid, err)
	}
	if uid, err := s.LastUID(ctx, account, "INBOX", 8); err != nil || uid != 0 {
		t.Errorf("LastUID(other validity) = %d, %v, want 0", uid, err)
	}

	// Tri : non triés d'abord à trier ; triage enregistré.
	pending, err := s.List(ctx, Query{Search: stamp, Untriaged: true})
	if err != nil || len(pending) != 3 {
		t.Fatalf("List(untriaged) = %d, %v", len(pending), err)
	}
	tr := Triage{Category: Action, Summary: "Payer la facture", Action: "Payer la facture", Model: "qwen3-8b", At: at(4), Version: TriageVersion}
	if err := s.SetTriage(ctx, facture.ID, tr); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTriage(ctx, stamp+"-promo", Triage{Error: "modèle indisponible", At: at(4), Version: TriageVersion}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTriage(ctx, stamp+"-absent", tr); err == nil {
		t.Error("SetTriage(unknown) = nil")
	}
	pending, _ = s.List(ctx, Query{Search: stamp, Untriaged: true})
	if len(pending) != 1 || pending[0].ID != stamp+"-réunion" {
		t.Errorf("List(untriaged) after triage = %+v, want only réunion (an error is not pending)", pending)
	}
	if got, _, _ := s.Get(ctx, facture.ID); got.Triage.Category != Action || got.Triage.Summary != "Payer la facture" || got.Triage.Model != "qwen3-8b" {
		t.Errorf("triage = %+v", got.Triage)
	}

	// Emails à répondre ; un tri d'une version plus ancienne (sans cette
	// analyse) est à refaire.
	if err := s.SetTriage(ctx, stamp+"-réunion", Triage{Category: Info, Summary: "Réunion jeudi", Reply: true, Question: "Confirmer jeudi", Version: TriageVersion}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Count(ctx, Query{Search: stamp, Untriaged: true}); err != nil || n != 0 {
		t.Errorf("Count(untriaged) = %d, %v, want 0", n, err)
	}
	if err := s.SetTriage(ctx, stamp+"-promo", Triage{Category: Newsletter, Summary: "Promo"}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.List(ctx, Query{Search: stamp, Untriaged: true}); len(pending) != 1 || pending[0].ID != stamp+"-promo" {
		t.Errorf("List(untriaged) = %+v, want the mail triaged by an older version", pending)
	}
	if n, err := s.Count(ctx, Query{Search: stamp}); err != nil || n != 3 {
		t.Errorf("Count = %d, %v, want 3", n, err)
	}
	if n, err := s.Count(ctx, Query{Search: stamp, Reply: true}); err != nil || n != 1 {
		t.Errorf("Count(reply) = %d, %v, want 1", n, err)
	}
	if got, _, _ := s.Get(ctx, stamp+"-réunion"); !got.Triage.Reply || got.Triage.Question != "Confirmer jeudi" || got.Triage.Version != TriageVersion {
		t.Errorf("triage = %+v", got.Triage)
	}

	// Liste : du plus récent au plus ancien ; recherche (objet, expéditeur,
	// texte, résumé) insensible à la casse et littérale ; catégorie.
	ids := func(ms []Mail) string {
		var out []string
		for _, m := range ms {
			if strings.HasPrefix(m.ID, stamp) {
				out = append(out, strings.TrimPrefix(m.ID, stamp+"-"))
			}
		}
		return strings.Join(out, ",")
	}
	for q, want := range map[Query]string{
		{Search: stamp}:                   "réunion,promo,facture",
		{Search: stamp, Category: Action}: "facture",
		{Search: "PAYER LA"}:              "facture",
		{Search: "corps de promo"}:        "promo",
		{Search: stamp + " ("}:            "",
		{Search: stamp, Limit: 1}:         "réunion",
		{Search: stamp, Reply: true}:      "réunion",
	} {
		ms, err := s.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(ms); got != want {
			t.Errorf("List(%+v) = %q, want %q", q, got, want)
		}
	}

	// Pièce jointe envoyée dans Documents.
	if err := s.SetAttachmentDoc(ctx, facture.ID, 0, "job-1"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, facture.ID); got.Attachments[0].DocID != "job-1" {
		t.Errorf("attachment doc = %+v", got.Attachments)
	}
	if err := s.SetAttachmentDoc(ctx, facture.ID, 5, "job-1"); err == nil {
		t.Error("SetAttachmentDoc(unknown index) = nil")
	}
}

func TestFakeStore_Contract(t *testing.T) {
	storeContract(t, NewFakeStore(), "fake")
}
