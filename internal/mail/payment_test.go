package mail

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

// Paiements repérés dans les emails : seuls les emails déjà triés par la
// version en vigueur, sans erreur, « à traiter » ou « document » sont lus.
func TestPaymentCandidate(t *testing.T) {
	ok := Triage{Category: Action, Version: TriageVersion}
	for name, c := range map[string]struct {
		t    Triage
		want bool
	}{
		"à traiter":        {ok, true},
		"document":         {Triage{Category: Document, Version: TriageVersion}, true},
		"newsletter":       {Triage{Category: Newsletter, Version: TriageVersion}, false},
		"information":      {Triage{Category: Info, Version: TriageVersion}, false},
		"ancienne version": {Triage{Category: Action, Version: TriageVersion - 1}, false},
		"tri en erreur":    {Triage{Category: Action, Error: "illisible", Version: TriageVersion}, false},
		"pas trié":         {Triage{}, false},
	} {
		if got := PaymentCandidate(Mail{Triage: c.t}); got != c.want {
			t.Errorf("%s: PaymentCandidate = %v, want %v", name, got, c.want)
		}
	}
}

func TestValidIBAN(t *testing.T) {
	for _, iban := range []string{"FR7630006000011234567890189", "DE89370400440532013000", normalizeIBAN("FR76 3000 6000 0112 3456 7890 189"), normalizeIBAN("fr76 3000 6000 0112 3456 7890 189")} {
		if !validIBAN(iban) {
			t.Errorf("validIBAN(%q) = false", iban)
		}
	}
	for _, iban := range []string{"FR7630006000011234567890188", "FR76300060", "1276300060000112345678901", "FR76300060000112345678901$9", ""} {
		if validIBAN(iban) {
			t.Errorf("validIBAN(%q) = true", iban)
		}
	}
}

const invoiceBody = "Bonjour,\nVotre facture F-2026-42 d'un montant de 1 234,56 € est à régler avant le 15/10/2026.\nIBAN : FR76 3000 6000 0112 3456 7890 189 — BIC AGRIFRPP\nPayer en ligne : https://pay.acme.fr/f42\nAcme SARL"

func invoiceMail() Mail {
	return Mail{
		ID: "m-pay", From: Address{Name: "Acme", Email: "compta@acme.fr"}, Subject: "Facture F-2026-42",
		Date: now, Text: invoiceBody, Triage: Triage{Category: Action, Version: TriageVersion},
	}
}

// goodReply : ce que le modèle rend pour invoiceMail.
const goodReply = `{"to_pay":true,"auto_debit":false,"payee":"Acme SARL","amount":"1 234,56","currency":"EUR","due_date":"2026-10-15","reference":"F-2026-42","iban":"FR76 3000 6000 0112 3456 7890 189","bic":"AGRIFRPP","method":"virement","link":"https://pay.acme.fr/f42","evidence":"d'un montant de 1 234,56 €","confidence":0.92}`

func analyzer(model *scriptedLLM) PaymentAnalyzer {
	return PaymentAnalyzer{LLM: model, Model: "qwen3-8b", Now: func() time.Time { return now }}
}

func TestPaymentAnalyzer_ExtractsPaymentInfo(t *testing.T) {
	model := &scriptedLLM{reply: goodReply}
	p, err := analyzer(model).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if err != nil {
		t.Fatal(err)
	}
	if !p.ToPay || p.AutoDebit || p.Payee != "Acme SARL" || p.Amount != "1 234,56" || p.Currency != "EUR" || p.DueDate != "2026-10-15" ||
		p.Reference != "F-2026-42" || p.IBAN != "FR7630006000011234567890189" || !p.IBANValid || p.BIC != "AGRIFRPP" ||
		p.Method != "virement" || p.Link != "https://pay.acme.fr/f42" || p.Evidence == "" || p.Confidence != 0.92 {
		t.Errorf("Payment = %+v", p)
	}
	if p.NeedsReview || len(p.Checks) != 0 {
		t.Errorf("NeedsReview = %v, checks %v, want a clean result", p.NeedsReview, p.Checks)
	}
	if p.Version != PaymentVersion || p.Model != "qwen3-8b" || p.Prompt != DefaultPaymentPrompt || !p.At.Equal(now) {
		t.Errorf("provenance = %+v", p)
	}
	if strings.Join(p.Sources, ",") != "corps" {
		t.Errorf("Sources = %v", p.Sources)
	}
	for _, want := range []string{"Facture F-2026-42", "compta@acme.fr", "1 234,56 €"} {
		if !strings.Contains(model.got.Text, want) {
			t.Errorf("text sent lacks %q", want)
		}
	}
	for _, want := range []string{`"to_pay"`, `"prelevement"`, `"virement"`, `"iban"`} {
		if !strings.Contains(string(model.got.Schema), want) {
			t.Errorf("schema lacks %s: %s", want, model.got.Schema)
		}
	}
}

func TestPaymentAnalyzer_ReadsPDFAttachmentText(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	m := invoiceMail()
	m.Text = "Bonjour, veuillez trouver la facture ci-jointe."
	m.Attachments = []Attachment{
		{Index: 0, Filename: "facture-42.pdf", ContentType: "application/pdf", Stored: true},
		{Index: 1, Filename: "logo.png", ContentType: "image/png", Stored: true},
	}
	store.Save(ctx, m, map[int][]byte{0: []byte("%PDF-1.4"), 1: []byte("PNG")})
	work := t.TempDir()
	model := &scriptedLLM{reply: goodReply}
	a := analyzer(model)
	a.WorkDir = work
	a.PDFText = triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: invoiceBody}}}
	p, err := a.Analyze(ctx, store, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.got.Text, "FR76 3000 6000") || !strings.Contains(model.got.Text, "facture-42.pdf") {
		t.Errorf("PDF text not sent to the model:\n%s", model.got.Text)
	}
	if strings.Join(p.Sources, ",") != "corps,facture-42.pdf" {
		t.Errorf("Sources = %v", p.Sources)
	}
	if p.IBAN == "" || p.NeedsReview {
		t.Errorf("IBAN from the PDF dropped: %+v", p)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestPaymentAnalyzer_ScannedAttachmentIsFlagged(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	m := invoiceMail()
	m.Attachments = []Attachment{{Index: 0, Filename: "scan.pdf", Stored: true}}
	store.Save(ctx, m, map[int][]byte{0: []byte("%PDF")})
	a := analyzer(&scriptedLLM{reply: goodReply})
	a.WorkDir = t.TempDir()
	a.PDFText = triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: "  "}}}
	p, err := a.Analyze(ctx, store, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Checks, "\n"), "sans texte") || !strings.Contains(strings.Join(p.Checks, "\n"), "scan.pdf") {
		t.Errorf("Checks = %v, want the scanned attachment flagged", p.Checks)
	}

	a.PDFText = triage.FakeExtractor{Err: errors.New("pdftotext absent")}
	p, err = a.Analyze(ctx, store, m)
	if err != nil || !strings.Contains(strings.Join(p.Checks, "\n"), "pdftotext absent") {
		t.Errorf("extractor error: %v, checks %v, want noted without failing", err, p.Checks)
	}
}

func TestPaymentAnalyzer_DropsIBANAndLinkNotInSource(t *testing.T) {
	reply := strings.NewReplacer("FR76 3000 6000 0112 3456 7890 189", "DE89370400440532013000", "https://pay.acme.fr/f42", "https://evil.example/pay").Replace(goodReply)
	p, err := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if err != nil {
		t.Fatal(err)
	}
	if p.IBAN != "" || p.Link != "" || !p.NeedsReview {
		t.Errorf("Payment = %+v, want invented IBAN and link dropped", p)
	}
	checks := strings.Join(p.Checks, "\n")
	if !strings.Contains(checks, "IBAN") || !strings.Contains(checks, "Lien") {
		t.Errorf("Checks = %v", p.Checks)
	}

	m := invoiceMail()
	m.Text = strings.Replace(m.Text, "https://", "http://", 1)
	reply = strings.Replace(goodReply, "https://", "http://", 1)
	if p, _ := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), m); p.Link != "" {
		t.Errorf("Link = %q, want a non-https link dropped", p.Link)
	}
}

func TestPaymentAnalyzer_InvalidIBANKeptForReview(t *testing.T) {
	m := invoiceMail()
	m.Text = strings.Replace(m.Text, "7890 189", "7890 188", 1)
	reply := strings.Replace(goodReply, "7890 189", "7890 188", 1)
	p, err := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), m)
	if err != nil {
		t.Fatal(err)
	}
	if p.IBAN != "FR7630006000011234567890188" || p.IBANValid || !p.NeedsReview {
		t.Errorf("Payment = %+v, want the IBAN kept, invalid, for review", p)
	}
}

func TestPaymentAnalyzer_AmountNotInSourceNeedsReview(t *testing.T) {
	reply := strings.Replace(goodReply, `"1 234,56"`, `"1234.56"`, 1)
	p, _ := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if p.NeedsReview {
		t.Errorf("1234.56 not found in « 1 234,56 € »: %v", p.Checks)
	}
	reply = strings.Replace(goodReply, `"1 234,56"`, `"999,00"`, 1)
	p, _ = analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if !p.NeedsReview || !strings.Contains(strings.Join(p.Checks, "\n"), "Montant") {
		t.Errorf("Payment = %+v, want an amount absent from the source for review", p)
	}
}

func TestPaymentAnalyzer_InvalidDueDateCleared(t *testing.T) {
	for _, due := range []string{"31/02", "2026-02-31", "demain"} {
		reply := strings.Replace(goodReply, `"2026-10-15"`, `"`+due+`"`, 1)
		p, err := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
		if err != nil {
			t.Fatal(err)
		}
		if p.DueDate != "" || !strings.Contains(strings.Join(p.Checks, "\n"), "chéance") {
			t.Errorf("%s: DueDate = %q, checks %v", due, p.DueDate, p.Checks)
		}
	}
}

func TestPaymentAnalyzer_AutoDebitIsNotToPay(t *testing.T) {
	reply := strings.Replace(strings.Replace(goodReply, `"auto_debit":false`, `"auto_debit":true`, 1), `"virement"`, `"prelevement"`, 1)
	p, _ := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if p.ToPay || !p.AutoDebit {
		t.Errorf("Payment = %+v, want an automatic debit not to pay", p)
	}
}

func TestPaymentAnalyzer_LowConfidenceNeedsReview(t *testing.T) {
	reply := strings.Replace(goodReply, `0.92`, `0.5`, 1)
	p, _ := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if !p.NeedsReview || !p.ToPay {
		t.Errorf("Payment = %+v, want low confidence for review, never silently accepted", p)
	}
}

func TestPaymentAnalyzer_EditablePrompt(t *testing.T) {
	model := &scriptedLLM{reply: goodReply}
	a := analyzer(model)
	a.Prompt = func() string { return "Mes règles" }
	if p, _ := a.Analyze(context.Background(), NewFakeStore(), invoiceMail()); p.Prompt != "Mes règles" || model.got.Prompt != "Mes règles" {
		t.Errorf("prompt = %q", model.got.Prompt)
	}
	a.Prompt = func() string { return " " }
	a.Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if model.got.Prompt != DefaultPaymentPrompt {
		t.Error("blank prompt: want the default")
	}
}

func TestPaymentAnalyzer_BadReplyAndModelError(t *testing.T) {
	for _, reply := range []string{`pas du json`, strings.Replace(goodReply, `"virement"`, `"bitcoin"`, 1)} {
		if _, err := analyzer(&scriptedLLM{reply: reply}).Analyze(context.Background(), NewFakeStore(), invoiceMail()); !errors.Is(err, errBadReply) {
			t.Errorf("reply %q: err = %v, want errBadReply", reply, err)
		}
	}
	_, err := analyzer(&scriptedLLM{err: errors.New("serveur arrêté")}).Analyze(context.Background(), NewFakeStore(), invoiceMail())
	if err == nil || errors.Is(err, errBadReply) || !strings.Contains(err.Error(), "serveur arrêté") {
		t.Errorf("err = %v", err)
	}
}

func TestPaymentTaskInput(t *testing.T) {
	m := Mail{Subject: "Facture 42", Payment: Payment{ToPay: true, Payee: "Acme", Amount: "120,00", Currency: "EUR", DueDate: "2026-10-15"}}
	got := PaymentTaskInput(m)
	if got != "Payer Acme 120,00 EUR 15/10/2026" {
		t.Errorf("PaymentTaskInput = %q", got)
	}
	if q := notes.ParseQuickAdd(got, now); q.Due != "2026-10-15" || q.Title != "Payer Acme 120,00 EUR" {
		t.Errorf("ParseQuickAdd = %+v, want the due date read", q)
	}
	m.Payment.DueDate = ""
	if got := PaymentTaskInput(m); got != "Payer Acme 120,00 EUR" {
		t.Errorf("without due date = %q", got)
	}
	m.Payment.Payee = ""
	if got := PaymentTaskInput(m); got != "Payer Facture 42 120,00 EUR" {
		t.Errorf("without payee = %q", got)
	}
}
