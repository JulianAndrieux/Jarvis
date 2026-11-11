package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Jalon 59 : le modèle local est un outil de l'orchestrateur, et une
// source peut n'être ouverte à Claude qu'à travers lui.

func TestParsePolicies(t *testing.T) {
	got, err := ParsePolicies(" notes, emails=summary ,documents=raw,memory ")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Policy{SourceNotes: PolicyRaw, SourceEmails: PolicySummary, SourceDocuments: PolicyRaw, SourceMemory: PolicyRaw}
	if len(got) != len(want) {
		t.Fatalf("policies = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, veut %q", k, got[k], v)
		}
	}
	for _, bad := range []string{"courriel", "emails=resume", "emails=", "=summary", "notes,notes=summary"} {
		if _, err := ParsePolicies(bad); err == nil {
			t.Errorf("ParsePolicies(%q) accepté", bad)
		}
	}
	if got, err := ParsePolicies(""); err != nil || len(got) != 0 {
		t.Errorf("vide = %v, %v", got, err)
	}
}

// fakeDigester : répond « vu(n caractères) » et garde ce qu'il a reçu.
type fakeDigester struct {
	questions []string
	texts     []string
	err       error
}

func (f *fakeDigester) Digest(ctx context.Context, question, text string) (string, error) {
	f.questions = append(f.questions, question)
	f.texts = append(f.texts, text)
	if f.err != nil {
		return "", f.err
	}
	return fmt.Sprintf("vu(%d)", len([]rune(text))), nil
}

func mailFixture(t *testing.T) *mail.Service {
	t.Helper()
	store := mail.NewFakeStore()
	ctx := context.Background()
	for _, m := range []mail.Mail{
		{ID: "m1", From: mail.Address{Name: "Paul Weber", Email: "paul@example.org"}, Subject: "Devis toiture", Date: testNow,
			Text:   "SECRET-DEBUT " + strings.Repeat("ligne du devis ", 1500) + " SECRET-FIN",
			Triage: mail.Triage{Summary: "Paul attend une confirmation.", Reply: true, Question: "Confirmer ?", Version: mail.TriageVersion}},
		{ID: "m2", From: mail.Address{Email: "banque@example.org"}, Subject: "Relevé", Date: testNow, Text: "Solde : 1 234,56 EUR"},
	} {
		if _, err := store.Save(ctx, m, nil); err != nil {
			t.Fatal(err)
		}
	}
	return &mail.Service{Store: store, DefaultScope: tenancy.LocalScope()}
}

// Les outils de lecture savent rendre leur contenu entier, sans tranche.
func TestReadTools_Full(t *testing.T) {
	tools := MailTools(mailFixture(t))
	var read Tool
	for _, tool := range tools {
		if tool.Name == "read_email" {
			read = tool
		}
	}
	if read.Content != ContentFull || read.Full == nil {
		t.Fatalf("read_email : Content=%q, Full nil=%v", read.Content, read.Full == nil)
	}
	full, err := read.Full(context.Background(), json.RawMessage(`{"id":"m1"}`))
	if err != nil || !strings.Contains(full, "SECRET-DEBUT") || !strings.Contains(full, "SECRET-FIN") ||
		!strings.Contains(full, "Devis toiture") || strings.Contains(full, "offset=") {
		t.Errorf("Full = %d caractères, %v", len(full), err)
	}
	// La liste, elle, se déclare comme telle.
	for _, tool := range tools {
		if tool.Name == "search_emails" && tool.Content != ContentList {
			t.Errorf("search_emails : Content=%q", tool.Content)
		}
		if tool.Name == "archive_email" && tool.Content != "" {
			t.Errorf("archive_email ne rend pas de contenu : %q", tool.Content)
		}
	}
}

func TestApplyPolicies_SummaryNeverReturnsRawText(t *testing.T) {
	local := &fakeDigester{}
	tools, err := ApplyPolicies(MailTools(mailFixture(t)), map[string]Policy{SourceEmails: PolicySummary}, local)
	if err != nil {
		t.Fatal(err)
	}
	var read Tool
	for _, tool := range tools {
		if tool.Name == "read_email" {
			read = tool
		}
	}
	props := read.Schema["properties"].(map[string]any)
	req, _ := read.Schema["required"].([]string)
	if props["question"] == nil || props["offset"] != nil || !strings.Contains(strings.Join(req, ","), "question") ||
		!strings.Contains(read.Description, "modèle local") {
		t.Fatalf("schéma sous résumé = %v, required %v\n%s", props, req, read.Description)
	}
	out, err := read.Run(context.Background(), json.RawMessage(`{"id":"m1","question":"Que demande Paul ?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "SECRET") || strings.Contains(out, "ligne du devis") {
		t.Fatalf("le texte brut est sorti malgré la politique :\n%.300s", out)
	}
	if !strings.Contains(out, "vu(") || !strings.Contains(out, "modèle local") {
		t.Errorf("sortie = %q", out)
	}
	// Tout le texte a été lu par le modèle local, par morceaux, avec la question.
	joined := strings.Join(local.texts, "")
	if len(local.texts) < 2 || !strings.Contains(joined, "SECRET-DEBUT") || !strings.Contains(joined, "SECRET-FIN") {
		t.Errorf("morceaux lus = %d, début/fin présents = %v/%v", len(local.texts),
			strings.Contains(joined, "SECRET-DEBUT"), strings.Contains(joined, "SECRET-FIN"))
	}
	for _, chunk := range local.texts {
		if len([]rune(chunk)) > digestChunk {
			t.Errorf("morceau de %d caractères, limite %d", len([]rune(chunk)), digestChunk)
		}
	}
	for _, q := range local.questions {
		if q != "Que demande Paul ?" {
			t.Errorf("question transmise = %q", q)
		}
	}
	if _, err := read.Run(context.Background(), json.RawMessage(`{"id":"m1"}`)); err == nil || !strings.Contains(err.Error(), "question") {
		t.Errorf("sans question = %v", err)
	}
	if _, err := read.Run(context.Background(), json.RawMessage(`{"id":"absent","question":"x"}`)); err == nil {
		t.Error("email absent accepté")
	}
	// Une panne du modèle local ne rabat jamais sur le texte brut.
	local.err = errors.New("modèle indisponible")
	if out, err := read.Run(context.Background(), json.RawMessage(`{"id":"m2","question":"x"}`)); err == nil || strings.Contains(out, "1 234,56") {
		t.Errorf("panne locale : out=%q err=%v", out, err)
	}
}

func TestApplyPolicies_OpenClosedAndRaw(t *testing.T) {
	svc, n := notesFixture(t)
	local := &fakeDigester{}
	all := append(NotesTools(svc), MailTools(mailFixture(t))...)

	// Source absente : aucun de ses outils.
	tools, err := ApplyPolicies(all, map[string]Policy{SourceNotes: PolicyRaw}, local)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Source != SourceNotes {
			t.Errorf("outil %s d'une source fermée", tool.Name)
		}
	}
	// Brut : le modèle local n'est jamais sollicité.
	out := runTool(t, tools, "read_note", `{"id":"`+n.ID+`"}`)
	if !strings.Contains(out, "Formulaire 100") || len(local.texts) != 0 {
		t.Errorf("politique brute : %q, appels locaux %d", out, len(local.texts))
	}
	// Résumé : les listes ne montrent plus d'extrait du texte.
	sum, err := ApplyPolicies(all, map[string]Policy{SourceNotes: PolicySummary}, local)
	if err != nil {
		t.Fatal(err)
	}
	list := runTool(t, sum, "search_notes", `{"query":"impôts"}`)
	if strings.Contains(list, "Formulaire 100") || !strings.Contains(list, "Déclaration d'impôts 2026") || !strings.Contains(list, n.ID) {
		t.Errorf("liste sous résumé :\n%s", list)
	}
	// Les outils d'écriture ne changent pas.
	for _, tool := range sum {
		if tool.Name == "create_note" && tool.Schema["properties"].(map[string]any)["question"] != nil {
			t.Error("create_note a reçu une question")
		}
	}
	// Résumé sans modèle local : refusé à la configuration, pas à l'usage.
	if _, err := ApplyPolicies(all, map[string]Policy{SourceEmails: PolicySummary}, nil); err == nil || !strings.Contains(err.Error(), "emails") {
		t.Errorf("résumé sans modèle local = %v", err)
	}
}

func TestDigestTool_BulkLocalReading(t *testing.T) {
	local := &fakeDigester{}
	tools, err := ApplyPolicies(MailTools(mailFixture(t)), map[string]Policy{SourceEmails: PolicySummary}, local)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestTool(tools, local)
	if digest == nil || digest.Level != LevelRead || digest.Name != "digest_locally" {
		t.Fatalf("digest = %+v", digest)
	}
	if kinds := digest.Schema["properties"].(map[string]any)["tool"].(map[string]any)["enum"].([]string); len(kinds) != 1 || kinds[0] != "read_email" {
		t.Errorf("outils lisibles = %v", kinds)
	}
	out, err := digest.Run(context.Background(), json.RawMessage(`{"tool":"read_email","ids":["m2","absent","m1"],"question":"Y a-t-il un montant ?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "SECRET") || strings.Contains(out, "1 234,56") {
		t.Fatalf("texte brut dans la sortie :\n%.300s", out)
	}
	for _, want := range []string{"[m2]", "[m1]", "[absent]", "introuvable", "vu("} {
		if !strings.Contains(out, want) {
			t.Errorf("sortie sans %q :\n%s", want, out)
		}
	}
	if strings.Index(out, "[m2]") > strings.Index(out, "[m1]") {
		t.Error("l'ordre demandé n'est pas gardé")
	}
	ids := make([]string, 0, maxDigestItems+1)
	for i := 0; i <= maxDigestItems; i++ {
		ids = append(ids, fmt.Sprintf(`"x%d"`, i))
	}
	if _, err := digest.Run(context.Background(), json.RawMessage(`{"tool":"read_email","ids":[`+strings.Join(ids, ",")+`],"question":"x"}`)); err == nil {
		t.Error("trop d'éléments acceptés")
	}
	if _, err := digest.Run(context.Background(), json.RawMessage(`{"tool":"read_note","ids":["a"],"question":"x"}`)); err == nil {
		t.Error("outil d'une source fermée accepté")
	}
	// Sans modèle local, ou sans rien à lire : pas d'outil.
	if DigestTool(tools, nil) != nil || DigestTool(MemoryTools(nil), local) != nil {
		t.Error("digest_locally proposé sans modèle local ou sans outil de lecture")
	}
}

func TestLLMDigester(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{0: {JSON: json.RawMessage(`{"answer":" Paul demande une confirmation. ","relevant":true}`)}}}
	acquired, released := 0, 0
	d := &LLMDigester{LLM: fake, Prompt: func() string { return "CONSIGNE" },
		Acquire: func(context.Context) (func(), error) { acquired++; return func() { released++ }, nil }}
	got, err := d.Digest(context.Background(), "Que demande Paul ?", "Bonjour, peux-tu confirmer ?")
	if err != nil || got != "Paul demande une confirmation." {
		t.Fatalf("Digest = %q, %v", got, err)
	}
	if acquired != 1 || released != 1 {
		t.Errorf("file des modèles : prise %d, rendue %d", acquired, released)
	}
	req := fake.Calls[0]
	if !strings.Contains(req.Prompt, "CONSIGNE") || !strings.Contains(req.Prompt, "Que demande Paul ?") || req.Text != "Bonjour, peux-tu confirmer ?" {
		t.Errorf("requête = %+v", req)
	}
	var schema map[string]any
	if json.Unmarshal(req.Schema, &schema) != nil || schema["properties"].(map[string]any)["answer"] == nil {
		t.Errorf("schéma = %s", req.Schema)
	}

	// Le texte ne répond pas : dit, sans rien inventer.
	fake = &llm.FakeClient{Results: map[int]llm.ExtractResult{0: {JSON: json.RawMessage(`{"answer":"","relevant":false}`)}}}
	d = &LLMDigester{LLM: fake}
	if got, err := d.Digest(context.Background(), "q", "t"); err != nil || got != notRelevant {
		t.Errorf("non pertinent = %q, %v", got, err)
	}
	if !strings.Contains(fake.Calls[0].Prompt, "donnée") {
		t.Errorf("la consigne par défaut doit dire que le texte est une donnée : %q", fake.Calls[0].Prompt)
	}
	fake = &llm.FakeClient{Results: map[int]llm.ExtractResult{0: {JSON: json.RawMessage(`pas du json`)}}}
	if _, err := (&LLMDigester{LLM: fake}).Digest(context.Background(), "q", "t"); err == nil {
		t.Error("réponse illisible acceptée")
	}
	d = &LLMDigester{LLM: fake, Acquire: func(context.Context) (func(), error) { return nil, errors.New("file fermée") }}
	if _, err := d.Digest(context.Background(), "q", "t"); err == nil || len(fake.Calls) != 1 {
		t.Errorf("file indisponible : err=%v, appels=%d", err, len(fake.Calls))
	}
}

// Un long texte dont un seul morceau répond : la réponse n'est pas noyée
// sous « le texte ne répond pas » répété pour chaque autre morceau.
type pickyDigester struct{ calls int }

func (p *pickyDigester) Digest(ctx context.Context, question, text string) (string, error) {
	p.calls++
	if strings.Contains(text, "AIGUILLE") {
		return "Le total est de 2 317,20 EUR.", nil
	}
	return notRelevant, nil
}

func TestDigest_KeepsOnlyWhatAnswers(t *testing.T) {
	text := strings.Repeat("foin ", 3000) + "AIGUILLE" + strings.Repeat(" foin", 3000)
	p := &pickyDigester{}
	out, err := digest(context.Background(), p, "total ?", text)
	if err != nil || out != "Le total est de 2 317,20 EUR." || p.calls < 3 {
		t.Errorf("digest = %q, %v (%d morceaux)", out, err, p.calls)
	}
	out, _ = digest(context.Background(), &pickyDigester{}, "total ?", strings.Repeat("foin ", 3000))
	if out != notRelevant {
		t.Errorf("rien ne répond : %q", out)
	}
	// Au-delà de la limite, la lecture s'arrête et le dit.
	out, _ = digest(context.Background(), &pickyDigester{}, "x", strings.Repeat("a", digestChunk*maxDigestChunks+10))
	if !strings.Contains(out, "lecture arrêtée") {
		t.Errorf("lecture tronquée non signalée : %q", out)
	}
}
