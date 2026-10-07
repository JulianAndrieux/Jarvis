package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/mail"
)

// Paiements repérés dans les emails (ticket « Ce que j'ai besoin de
// payer ») : filtre « À payer », bloc de paiement de la fiche, marque
// « payé », barre latérale.

func acmePayment() mail.Payment {
	return mail.Payment{
		ToPay: true, Payee: "Acme", Amount: "120,00", Currency: "EUR", DueDate: "2026-10-15", Reference: "F-42",
		IBAN: "FR7630006000011234567890189", IBANValid: true, BIC: "AGRIFRPP", Method: "virement",
		Link: "https://pay.acme.fr/f42", Evidence: "Montant dû : 120,00 €", Confidence: 0.9,
		Sources: []string{"corps", "facture 42.pdf"}, Version: mail.PaymentVersion, Model: "qwen3-8b",
	}
}

// newPaymentServer : la facture d'Acme à payer, plus deux autres emails à
// payer (EDF, échéance plus proche ; impôts, sans échéance) et une facture
// déjà payée.
func newPaymentServer(t *testing.T) (*Server, *mail.FakeStore) {
	t.Helper()
	s, store, _ := newMailServer(t)
	ctx := context.Background()
	if err := store.SetPayment(ctx, "m-facture", acmePayment()); err != nil {
		t.Fatal(err)
	}
	add := func(id, subject string, p mail.Payment, paid bool) {
		store.Save(ctx, mail.Mail{ID: id, Subject: subject, From: mail.Address{Email: "factures@" + id + ".fr"}, Date: notesNow.Add(-5 * time.Hour),
			Triage: mail.Triage{Category: mail.Document, Version: mail.TriageVersion}}, nil)
		store.SetPayment(ctx, id, p)
		if paid {
			store.SetPaid(ctx, id, true, notesNow)
		}
	}
	add("m-edf", "Facture EDF septembre", mail.Payment{ToPay: true, Payee: "EDF", Amount: "64,10", Currency: "EUR", DueDate: "2026-10-01", Confidence: 0.9, Version: mail.PaymentVersion}, false)
	add("m-impots", "Avis taxe foncière", mail.Payment{ToPay: true, Payee: "DGFiP", Amount: "812", Currency: "EUR", Confidence: 0.9, Version: mail.PaymentVersion}, false)
	add("m-payee", "Facture déjà réglée", mail.Payment{ToPay: true, Payee: "Orange", Amount: "30", Currency: "EUR", DueDate: "2026-09-25", Version: mail.PaymentVersion}, true)
	return s, store
}

func TestEmails_PayFilterListsOnlyMailsToPay(t *testing.T) {
	s, _ := newPaymentServer(t)
	page := do(s, http.MethodGet, "/emails?cat=payer", nil).Body.String()
	for _, want := range []string{"Votre facture n° 42", "Facture EDF septembre", "Avis taxe foncière", "120,00 EUR", "15/10", "64,10 EUR", "01/10"} {
		if !strings.Contains(page, want) {
			t.Errorf("« À payer » lacks %q", want)
		}
	}
	for _, hidden := range []string{"Facture déjà réglée", "Soldes", "Dîner samedi"} {
		if strings.Contains(page, hidden) {
			t.Errorf("« À payer » shows %q", hidden)
		}
	}
	edf, acme, impots := strings.Index(page, "Facture EDF"), strings.Index(page, "Votre facture n° 42"), strings.Index(page, "Avis taxe")
	if !(edf < acme && acme < impots) {
		t.Errorf("order EDF %d, Acme %d, impôts %d: want by due date, no due date last", edf, acme, impots)
	}
	if def := do(s, http.MethodGet, "/emails", nil).Body.String(); !strings.Contains(def, "💶 À payer") || !strings.Contains(def, `cat=payer`) {
		t.Error("filters lack « À payer »")
	}
}

func TestEmail_ShowsPaymentBlock(t *testing.T) {
	s, store := newPaymentServer(t)
	p := acmePayment()
	p.Payee = "<script>alert(1)</script> Acme"
	p.NeedsReview = true
	p.Checks = []string{"Montant introuvable tel quel dans le texte : à vérifier"}
	store.SetPayment(context.Background(), "m-facture", p)
	page := do(s, http.MethodGet, "/emails/m-facture", nil).Body.String()
	for _, want := range []string{"💶 Paiement", "&lt;script&gt;alert(1)&lt;/script&gt; Acme", "120,00", "EUR", "15/10/2026", "F-42",
		"FR76 3000 6000 0112 3456 7890 189", "AGRIFRPP", "Montant dû : 120,00 €", "à vérifier", "Montant introuvable tel quel",
		"https://pay.acme.fr/f42", "vérifie", "facture 42.pdf", "Marquer comme payé"} {
		if !strings.Contains(page, want) {
			t.Errorf("payment block lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>alert(1)</script> Acme") {
		t.Error("payee not escaped")
	}
	if strings.Contains(page, `href="https://pay.acme.fr`) {
		t.Error("payment link is clickable: it comes from an email")
	}
}

func TestEmail_PaymentTaskPrefilled(t *testing.T) {
	s, _ := newPaymentServer(t)
	page := do(s, http.MethodGet, "/emails/m-facture", nil).Body.String()
	if !strings.Contains(page, `value="Payer Acme 120,00 EUR 15/10/2026"`) {
		t.Error("task input not prefilled with the payment")
	}
}

func TestEmail_MarkPaidAndUnpaid(t *testing.T) {
	s, store := newPaymentServer(t)
	ctx := context.Background()
	rec := do(s, http.MethodPost, "/emails/m-facture/paid", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/emails/m-facture" {
		t.Fatalf("paid: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if m, _, _ := store.Get(ctx, "m-facture"); !m.Paid {
		t.Fatal("mail not marked paid")
	}
	if page := do(s, http.MethodGet, "/emails/m-facture", nil).Body.String(); !strings.Contains(page, "Payé le") {
		t.Error("detail does not say it is paid")
	}
	if list := do(s, http.MethodGet, "/emails?cat=payer", nil).Body.String(); strings.Contains(list, "Votre facture n° 42") {
		t.Error("paid mail still « À payer »")
	}
	if rec := do(s, http.MethodPost, "/emails/m-facture/unpaid", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("unpaid: %d", rec.Code)
	}
	if m, _, _ := store.Get(ctx, "m-facture"); m.Paid {
		t.Error("mail still paid")
	}

	// Depuis la liste (HTMX) : réponse vide, la ligne disparaît.
	if list := do(s, http.MethodGet, "/emails?cat=payer", nil).Body.String(); !strings.Contains(list, `hx-post="/emails/m-edf/paid"`) {
		t.Error("list lacks the paid button")
	}
	req := httptest.NewRequest(http.MethodPost, "/emails/m-edf/paid", nil)
	req.Header.Set("HX-Request", "true")
	hx := httptest.NewRecorder()
	s.Routes().ServeHTTP(hx, req)
	if hx.Code != http.StatusOK || hx.Body.Len() != 0 {
		t.Errorf("paid (htmx): %d %q", hx.Code, hx.Body.String())
	}
	if m, _, _ := store.Get(ctx, "m-edf"); !m.Paid {
		t.Error("m-edf not paid")
	}
	if rec := do(s, http.MethodPost, "/emails/inconnu/paid", nil); rec.Code != http.StatusNotFound {
		t.Errorf("paid(unknown): %d", rec.Code)
	}
}

func TestEmail_NoPaymentBlockForNonCandidate(t *testing.T) {
	s, _ := newPaymentServer(t)
	for _, id := range []string{"m-promo", "m-alice"} {
		if page := do(s, http.MethodGet, "/emails/"+id, nil).Body.String(); strings.Contains(page, "💶 Paiement") {
			t.Errorf("%s shows a payment block", id)
		}
	}
}

func TestBuildPayments(t *testing.T) {
	due := func(days int) string { return notesNow.AddDate(0, 0, days).Format("2006-01-02") }
	mk := func(id, payee, d string) mail.Mail {
		return mail.Mail{ID: id, Subject: "Objet " + id, Payment: mail.Payment{ToPay: true, Payee: payee, Amount: "10", Currency: "EUR", DueDate: d}}
	}
	mails := []mail.Mail{
		mk("later", "Loin", due(30)),
		mk("soon", "Bientôt", due(3)),
		mk("late", "Retard", due(-2)),
		mk("nodue", "", ""),
	}
	paid := mk("paid", "Payé", due(1))
	paid.Paid = true
	archived := mk("arch", "Archivé", due(1))
	archived.Archived = true
	notToPay := mk("auto", "Prélèvement", due(1))
	notToPay.Payment.ToPay = false
	mails = append(mails, paid, archived, notToPay)

	items, more := buildPayments(notesNow, mails)
	var got []string
	for _, it := range items {
		got = append(got, strings.TrimPrefix(it.Href, "/emails/"))
	}
	if strings.Join(got, ",") != "late,soon,nodue" || more != 0 {
		t.Errorf("payments = %v (+%d), want late,soon,nodue", got, more)
	}
	if items[0].Title != "Retard" || !strings.Contains(items[0].Detail, "10 EUR") || !strings.Contains(items[0].Detail, "retard") {
		t.Errorf("late item = %+v", items[0])
	}
	if items[2].Title != "Objet nodue" {
		t.Errorf("no payee: title = %q, want the subject", items[2].Title)
	}

	var many []mail.Mail
	for i := 0; i < sidebarMax+3; i++ {
		many = append(many, mk(string(rune('a'+i)), "P", due(1)))
	}
	if items, more := buildPayments(notesNow, many); len(items) != sidebarMax || more != 3 {
		t.Errorf("limit: %d items, +%d", len(items), more)
	}
}

func TestSidebar_ShowsPayments(t *testing.T) {
	s, _ := newPaymentServer(t)
	body := do(s, http.MethodGet, "/sidebar", nil).Body.String()
	for _, want := range []string{"💶 À payer", "EDF", "64,10 EUR", "DGFiP", `href="/emails/m-edf"`, `href="/emails?cat=payer"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sidebar lacks %q", want)
		}
	}
	if strings.Contains(body, "Orange") {
		t.Error("sidebar lists a paid mail")
	}
}
