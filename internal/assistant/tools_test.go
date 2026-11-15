package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/mcp"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

var testNow = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

// fixture : une boîte à outils sur de vrais services, à stockage en
// mémoire, avec un document, deux emails et une tâche.
type fixture struct {
	box   *Toolbox
	jobs  *webapp.FakeStore
	mails *mail.FakeStore
	steps []Step
	tools map[string]mcp.Tool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{jobs: webapp.NewFakeStore(), mails: mail.NewFakeStore()}
	now := func() time.Time { return testNow }
	f.box = &Toolbox{
		Notes: &notes.Service{Store: notes.NewFakeStore(), Now: now},
		Mail:  &mail.Service{Store: f.mails, Now: now},
		Jobs:  webapp.NewJobManager(f.jobs, nil),
		Now:   now,
	}
	if _, err := f.jobs.Create(ctx, webapp.Job{
		ID: "doc1", Filename: "contrat-habitation.pdf", DocType: "correspondance", Status: webapp.StatusDone,
		CreatedAt: testNow.Add(-48 * time.Hour), Tags: []string{"assurance"},
		SearchText: "Contrat multirisque habitation. Franchise dégât des eaux : 150 EUR.",
		Result:     &pipeline.Result{Merged: extraction.MergedResult{JSON: json.RawMessage(`{"objet":{"value":"Contrat habitation","confidence":0.9}}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []mail.Mail{
		{ID: "m1", From: mail.Address{Name: "Acme", Email: "compta@acme.example"}, Subject: "Facture 2026-0042", Date: testNow.Add(-2 * time.Hour),
			Text:    "Merci de régler 120,00 EUR avant le 30/11.",
			Triage:  mail.Triage{Category: mail.Document, Summary: "Facture à régler", Version: mail.TriageVersion},
			Payment: mail.Payment{ToPay: true, Payee: "Acme", Amount: "120,00", Currency: "EUR", DueDate: "2026-11-30", IBAN: "FR7630006000011234567890189", IBANValid: true}},
		{ID: "m2", From: mail.Address{Email: "paul@example.org"}, Subject: "Devis toiture", Date: testNow.Add(-time.Hour),
			Text:   "Peux-tu me confirmer la date des travaux ?",
			Triage: mail.Triage{Category: mail.Action, Reply: true, Question: "Confirmer la date des travaux", Version: mail.TriageVersion}},
	} {
		if _, err := f.mails.Save(ctx, m, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.tools = map[string]mcp.Tool{}
	for _, tool := range f.box.Tools(func(s Step) { f.steps = append(f.steps, s) }) {
		f.tools[tool.Name] = tool
	}
	return f
}

// call appelle un outil et décode son résultat JSON.
func (f *fixture) call(t *testing.T, name, argsJSON string) (map[string]any, error) {
	t.Helper()
	tool, ok := f.tools[name]
	if !ok {
		t.Fatalf("outil %s absent", name)
	}
	text, err := tool.Call(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if jerr := json.Unmarshal([]byte(text), &out); jerr != nil {
		t.Fatalf("%s : résultat illisible : %s", name, text)
	}
	return out, nil
}

func (f *fixture) must(t *testing.T, name, argsJSON string) map[string]any {
	t.Helper()
	out, err := f.call(t, name, argsJSON)
	if err != nil {
		t.Fatalf("%s(%s) : %v", name, argsJSON, err)
	}
	return out
}

func (f *fixture) last() Step { return f.steps[len(f.steps)-1] }

// Ce que l'assistant ne doit jamais pouvoir faire n'est pas offert, et
// chaque outil décrit ses arguments.
func TestToolbox_OffersNoDestructiveTool(t *testing.T) {
	f := newFixture(t)
	if len(f.tools) < 12 {
		t.Fatalf("outils = %d", len(f.tools))
	}
	for name, tool := range f.tools {
		for _, banned := range []string{"delete", "ticket", "send", "pay_", "reprocess", "fetch", "web"} {
			if strings.Contains(name, banned) {
				t.Errorf("outil interdit offert : %s", name)
			}
		}
		if tool.Description == "" || tool.Schema["type"] != "object" {
			t.Errorf("%s : description ou schéma manquant", name)
		}
	}
}

func TestToolbox_NilServiceRemovesItsTools(t *testing.T) {
	box := &Toolbox{Notes: &notes.Service{Store: notes.NewFakeStore()}}
	for _, tool := range box.Tools(func(Step) {}) {
		if strings.Contains(tool.Name, "email") || strings.Contains(tool.Name, "document") {
			t.Errorf("outil sans service : %s", tool.Name)
		}
	}
	if len((&Toolbox{}).Tools(func(Step) {})) != 0 {
		t.Error("une boîte vide offre des outils")
	}
}

func TestToolbox_SearchAndReadDocument(t *testing.T) {
	f := newFixture(t)
	out := f.must(t, "search_documents", `{"query":"franchise"}`)
	docs := out["documents"].([]any)
	if len(docs) != 1 || docs[0].(map[string]any)["id"] != "doc1" {
		t.Fatalf("recherche dans le texte = %v", docs)
	}
	if s := f.last(); s.Tool != "search_documents" || s.Action || s.Failed || s.Link != "/documents" {
		t.Errorf("étape = %+v", s)
	}
	if out := f.must(t, "search_documents", `{"query":"introuvable"}`); len(out["documents"].([]any)) != 0 {
		t.Errorf("recherche sans résultat = %v", out)
	}

	out = f.must(t, "read_document", `{"id":"doc1"}`)
	if !strings.Contains(out["text"].(string), "150 EUR") || out["more_text"] != false {
		t.Errorf("texte = %v", out)
	}
	fields := out["extracted_fields"].(map[string]any)
	if fields["objet"].(map[string]any)["value"] != "Contrat habitation" {
		t.Errorf("champs extraits = %v", fields)
	}
	if s := f.last(); s.Link != "/documents/doc1" || !strings.Contains(s.Summary, "contrat-habitation.pdf") {
		t.Errorf("étape = %+v", s)
	}
}

// Un texte long se lit par tranches : la première dit qu'il en reste.
func TestToolbox_ReadDocumentInWindows(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("é", maxText) + "FIN"
	if _, err := f.jobs.Create(context.Background(), webapp.Job{ID: "long", Filename: "long.pdf", Status: webapp.StatusDone, SearchText: long}); err != nil {
		t.Fatal(err)
	}
	out := f.must(t, "read_document", `{"id":"long"}`)
	if out["more_text"] != true || out["text_chars"] != float64(maxText+3) || strings.Contains(out["text"].(string), "FIN") {
		t.Errorf("première tranche : more=%v chars=%v", out["more_text"], out["text_chars"])
	}
	out = f.must(t, "read_document", `{"id":"long","offset":12000}`)
	if out["text"] != "FIN" || out["more_text"] != false {
		t.Errorf("seconde tranche = %v", out["text"])
	}
}

func TestToolbox_UnknownIDAndArgumentAreErrorsRecordedAsFailed(t *testing.T) {
	f := newFixture(t)
	if _, err := f.call(t, "read_document", `{"id":"absent"}`); err == nil || !strings.Contains(err.Error(), "introuvable") {
		t.Errorf("document absent : %v", err)
	}
	if s := f.last(); !s.Failed || s.Error == "" || s.Tool != "read_document" {
		t.Errorf("étape d'échec = %+v", s)
	}
	if _, err := f.call(t, "read_document", `{"identifiant":"doc1"}`); err == nil {
		t.Error("argument inconnu accepté")
	}
	if _, err := f.call(t, "search_emails", `{"filter":"urgent"}`); err == nil {
		t.Error("filtre inconnu accepté")
	}
}

func TestToolbox_TagAndCommentDocument(t *testing.T) {
	f := newFixture(t)
	f.must(t, "tag_document", `{"id":"doc1","tags":["assurance"," habitation ","assurance",""]}`)
	f.must(t, "comment_document", `{"id":"doc1","comment":"Franchise 150 EUR"}`)
	j, _, _ := f.jobs.Get(context.Background(), "doc1")
	if strings.Join(j.Tags, ",") != "assurance,habitation" || j.Comment != "Franchise 150 EUR" {
		t.Errorf("document = tags %v, commentaire %q", j.Tags, j.Comment)
	}
	if s := f.last(); !s.Action || s.Link != "/documents/doc1" {
		t.Errorf("étape = %+v", s)
	}
}

func TestToolbox_SearchAndReadEmails(t *testing.T) {
	f := newFixture(t)
	out := f.must(t, "search_emails", `{}`)
	all := out["emails"].([]any)
	if len(all) != 2 || all[0].(map[string]any)["id"] != "m2" {
		t.Fatalf("tous les emails (récent d'abord) = %v", all)
	}
	pay := f.must(t, "search_emails", `{"filter":"to_pay"}`)["emails"].([]any)
	if len(pay) != 1 || pay[0].(map[string]any)["to_pay"] != "Acme 120,00 EUR 2026-11-30" {
		t.Errorf("à payer = %v", pay)
	}
	reply := f.must(t, "search_emails", `{"filter":"to_reply"}`)["emails"].([]any)
	if len(reply) != 1 || reply[0].(map[string]any)["question"] != "Confirmer la date des travaux" {
		t.Errorf("à répondre = %v", reply)
	}

	out = f.must(t, "read_email", `{"id":"m1"}`)
	p := out["payment"].(map[string]any)
	if p["iban"] != "FR7630006000011234567890189" || p["due_date"] != "2026-11-30" || !strings.Contains(out["text"].(string), "120,00") {
		t.Errorf("email lu = %v", out)
	}
	if out := f.must(t, "read_email", `{"id":"m2"}`); out["payment"] != nil {
		t.Errorf("paiement inventé sur un email sans paiement : %v", out["payment"])
	}
}

func TestToolbox_ArchiveAndMarkPaid(t *testing.T) {
	f := newFixture(t)
	f.must(t, "archive_email", `{"id":"m2"}`)
	f.must(t, "mark_email_paid", `{"id":"m1"}`)
	ctx := context.Background()
	if m, _, _ := f.mails.Get(ctx, "m2"); !m.Archived {
		t.Error("m2 pas archivé")
	}
	if m, _, _ := f.mails.Get(ctx, "m1"); !m.Paid || !m.PaidAt.Equal(testNow) {
		t.Errorf("m1 pas marqué payé : %+v", m.PaidAt)
	}
	if pay := f.must(t, "search_emails", `{"filter":"to_pay"}`)["emails"].([]any); len(pay) != 0 {
		t.Errorf("encore à payer après la marque : %v", pay)
	}
}

func TestToolbox_TaskLifecycle(t *testing.T) {
	f := newFixture(t)
	out := f.must(t, "create_task", `{"title":"Payer Acme demain","due":"2026-11-30","priority":"haute","email_id":"m1"}`)
	task := out["task"].(map[string]any)
	id := task["id"].(string)
	// Le titre n'est pas relu par la saisie rapide : « demain » y reste.
	if task["title"] != "Payer Acme demain" || task["due"] != "2026-11-30" || task["priority"] != "haute" || task["email_id"] != "m1" {
		t.Fatalf("tâche créée = %v", task)
	}
	if s := f.last(); !s.Action || s.Summary != "Tâche créée : Payer Acme demain (2026-11-30)" {
		t.Errorf("étape = %+v", s)
	}

	// Seul le champ donné change.
	out = f.must(t, "update_task", `{"id":"`+id+`","due":"2026-12-05"}`)
	if got := out["task"].(map[string]any); got["due"] != "2026-12-05" || got["title"] != "Payer Acme demain" || got["priority"] != "haute" {
		t.Errorf("après changement d'échéance = %v", got)
	}
	out = f.must(t, "update_task", `{"id":"`+id+`","due":""}`)
	if got := out["task"].(map[string]any); got["due"] != nil {
		t.Errorf("échéance pas retirée = %v", got)
	}

	list := f.must(t, "list_tasks", `{}`)
	if tasks := list["tasks"].([]any); len(tasks) != 1 || list["today"] != "2026-11-15" {
		t.Fatalf("liste = %v", list)
	}
	f.must(t, "complete_task", `{"id":"`+id+`"}`)
	// Cocher deux fois ne décoche pas.
	out = f.must(t, "complete_task", `{"id":"`+id+`"}`)
	if out["task"].(map[string]any)["done"] != true {
		t.Errorf("tâche décochée par un second appel : %v", out)
	}
	if tasks := f.must(t, "list_tasks", `{}`)["tasks"].([]any); len(tasks) != 0 {
		t.Errorf("tâche faite encore listée : %v", tasks)
	}
	if tasks := f.must(t, "list_tasks", `{"include_done":true}`)["tasks"].([]any); len(tasks) != 1 {
		t.Errorf("include_done = %v", tasks)
	}
}

// Un identifiant inventé par le modèle ne crée pas un lien mort.
func TestToolbox_CreateTaskRefusesUnknownLinkAndBadDue(t *testing.T) {
	f := newFixture(t)
	for _, bad := range []string{
		`{"title":"x","email_id":"inconnu"}`,
		`{"title":"x","document_id":"inconnu"}`,
		`{"title":"x","note_id":"inconnu"}`,
		`{"title":"x","due":"30/11/2026"}`,
		`{"title":"  "}`,
	} {
		if _, err := f.call(t, "create_task", bad); err == nil {
			t.Errorf("accepté : %s", bad)
		}
	}
	if tasks := f.must(t, "list_tasks", `{"include_done":true}`)["tasks"].([]any); len(tasks) != 0 {
		t.Errorf("une tâche refusée a été créée : %v", tasks)
	}
}

func TestToolbox_NoteLifecycle(t *testing.T) {
	f := newFixture(t)
	out := f.must(t, "create_note", `{"title":"Assurance habitation","text":"Franchise dégât des eaux : **150 EUR**.","document_id":"doc1"}`)
	id := out["id"].(string)
	if s := f.last(); !s.Action || s.Link != "/notes/"+id {
		t.Errorf("étape = %+v", s)
	}
	f.must(t, "append_to_note", `{"id":"`+id+`","text":"Numéro du contrat : H-778."}`)

	note := f.must(t, "read_note", `{"id":"`+id+`"}`)
	text := note["text"].(string)
	if note["title"] != "Assurance habitation" || !strings.Contains(text, "150 EUR") || !strings.Contains(text, "H-778") {
		t.Errorf("note = %v", note)
	}
	if docs := note["document_ids"].([]any); len(docs) != 1 || docs[0] != "doc1" {
		t.Errorf("document lié = %v", docs)
	}
	found := f.must(t, "search_notes", `{"query":"H-778"}`)["notes"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["id"] != id {
		t.Errorf("recherche = %v", found)
	}
	if _, err := f.call(t, "create_note", `{"title":"x","text":" "}`); err == nil {
		t.Error("note vide acceptée")
	}
	if _, err := f.call(t, "append_to_note", `{"id":"absente","text":"x"}`); err == nil {
		t.Error("ajout à une note absente accepté")
	}
}
