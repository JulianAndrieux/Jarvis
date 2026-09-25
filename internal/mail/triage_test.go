package mail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

// scriptedLLM renvoie une réponse fixée et garde la requête reçue.
type scriptedLLM struct {
	reply string
	err   error
	got   llm.ExtractRequest
}

func (s *scriptedLLM) Extract(ctx context.Context, req llm.ExtractRequest) (llm.ExtractResult, error) {
	s.got = req
	if s.err != nil {
		return llm.ExtractResult{}, s.err
	}
	return llm.ExtractResult{JSON: json.RawMessage(s.reply)}, nil
}

func sampleMail() Mail {
	return Mail{
		ID: "m1", From: Address{Name: "Acme", Email: "compta@acme.fr"}, Subject: "Facture n° 42",
		Date: time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC), Text: "Bonjour, veuillez trouver la facture. " + strings.Repeat("x", 9000),
		Attachments: []Attachment{{Filename: "facture-42.pdf"}},
	}
}

func TestTriager_ClassifiesAndSummarizes(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"document","summary":"Acme envoie la facture 42.","action":"Payer la facture 42 d'Acme"}`}
	tr := Triager{LLM: model, Model: "qwen3-8b", Now: func() time.Time { return now }}
	got, err := tr.Triage(context.Background(), sampleMail())
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != Document || got.Summary != "Acme envoie la facture 42." || got.Action != "Payer la facture 42 d'Acme" || got.Model != "qwen3-8b" || !got.At.Equal(now) || got.Prompt == "" {
		t.Errorf("Triage = %+v", got)
	}
	req := model.got
	for _, want := range []string{"Acme <compta@acme.fr>", "Facture n° 42", "facture-42.pdf", "veuillez trouver"} {
		if !strings.Contains(req.Text, want) {
			t.Errorf("text sent to the model lacks %q", want)
		}
	}
	if len(req.Text) > 7000 {
		t.Errorf("text sent = %d bytes, want the body truncated", len(req.Text))
	}
	var schema map[string]any
	if err := json.Unmarshal(req.Schema, &schema); err != nil || !strings.Contains(string(req.Schema), `"a_traiter"`) {
		t.Errorf("schema = %s, %v", req.Schema, err)
	}
}

func TestTriager_EditablePrompt(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"information","summary":"s","action":""}`}
	tr := Triager{LLM: model, Prompt: func() string { return "Mes règles de tri" }}
	got, err := tr.Triage(context.Background(), sampleMail())
	if err != nil || model.got.Prompt != "Mes règles de tri" || got.Prompt != "Mes règles de tri" {
		t.Errorf("prompt = %q, %v", model.got.Prompt, err)
	}
	tr.Prompt = func() string { return "  " }
	tr.Triage(context.Background(), sampleMail())
	if model.got.Prompt != DefaultTriagePrompt {
		t.Error("blank prompt: want the default")
	}
}

func TestTriager_RejectsUnknownCategoryAndModelErrors(t *testing.T) {
	for _, reply := range []string{`{"category":"spam","summary":"s"}`, `pas du json`} {
		tr := Triager{LLM: &scriptedLLM{reply: reply}}
		if _, err := tr.Triage(context.Background(), sampleMail()); err == nil {
			t.Errorf("reply %q accepted", reply)
		}
	}
	tr := Triager{LLM: &scriptedLLM{err: errors.New("serveur arrêté")}}
	if _, err := tr.Triage(context.Background(), sampleMail()); err == nil || !strings.Contains(err.Error(), "serveur arrêté") {
		t.Errorf("err = %v", err)
	}
}

func TestCategoryLabels(t *testing.T) {
	for _, c := range Categories {
		if CategoryLabel(c) == "" || CategoryLabel(c) == string(c) {
			t.Errorf("category %q has no label", c)
		}
	}
	if CategoryLabel("") != "À trier" {
		t.Errorf("CategoryLabel(\"\") = %q", CategoryLabel(""))
	}
}

// Emails à répondre : le modèle dit si une réponse est attendue et ce
// qu'on demande ; il lit pour cela la place de l'utilisateur parmi les
// destinataires.
func TestTriager_DetectsExpectedReply(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"a_traiter","summary":"Alice propose une réunion.","action":"Répondre à Alice","question":"Confirmer la réunion de jeudi","reply":true}`}
	tr := Triager{LLM: model}
	m := Mail{Account: "moi@gmail.com", From: Address{Name: "Alice", Email: "alice@example.com"}, To: []Address{{Email: "Moi@gmail.com"}}, Cc: []Address{{Email: "bob@example.com"}}, Subject: "Réunion", Text: "Tu es dispo jeudi ?"}
	got, err := tr.Triage(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Reply || got.Question != "Confirmer la réunion de jeudi" || got.Version != TriageVersion {
		t.Errorf("Triage = %+v, want a reply expected", got)
	}
	for _, want := range []string{"À : Moi@gmail.com", "Cc : bob@example.com", "destinataire direct"} {
		if !strings.Contains(model.got.Text, want) {
			t.Errorf("text sent to the model lacks %q:\n%s", want, model.got.Text)
		}
	}
	if !strings.Contains(string(model.got.Schema), `"reply"`) || !strings.Contains(string(model.got.Schema), `"question"`) {
		t.Errorf("schema lacks reply/question: %s", model.got.Schema)
	}

	m.To, m.Cc = []Address{{Email: "alice@example.com"}}, []Address{{Email: "moi@gmail.com"}}
	tr.Triage(context.Background(), m)
	if !strings.Contains(model.got.Text, "seulement en copie") {
		t.Errorf("Cc only not said to the model:\n%s", model.got.Text)
	}
	m.Cc = nil
	tr.Triage(context.Background(), m)
	if !strings.Contains(model.got.Text, "n'apparaît pas") {
		t.Errorf("absent recipient not said to the model:\n%s", model.got.Text)
	}
}

// Un expéditeur automatique n'attend jamais de réponse, quoi que dise le
// modèle (un modèle local de 8B se laisse prendre aux « Répondez avant le… »).
func TestTriager_AutomatedSenderNeverExpectsAReply(t *testing.T) {
	for _, from := range []string{"no-reply@amazon.fr", "noreply@github.com", "Ne-Pas-Repondre@impots.gouv.fr", "donotreply@sncf.fr", "notifications@linkedin.com", "MAILER-DAEMON@google.com"} {
		model := &scriptedLLM{reply: `{"category":"a_traiter","summary":"s","action":"","question":"Confirmer","reply":true}`}
		got, err := Triager{LLM: model}.Triage(context.Background(), Mail{From: Address{Email: from}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Reply || got.Question != "" {
			t.Errorf("%s: Reply = %v, question %q, want no reply expected", from, got.Reply, got.Question)
		}
	}
	model := &scriptedLLM{reply: `{"category":"a_traiter","summary":"s","action":"","question":"Confirmer","reply":true}`}
	if got, _ := (Triager{LLM: model}).Triage(context.Background(), Mail{From: Address{Email: "noemie@replyco.fr"}}); !got.Reply {
		t.Error("a person's address was taken for an automated sender")
	}
}
