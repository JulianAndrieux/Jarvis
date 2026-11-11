package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

func runTool(t *testing.T, tools []Tool, name, input string) string {
	t.Helper()
	out, err := tryTool(tools, name, input)
	if err != nil {
		t.Fatalf("%s(%s) : %v", name, input, err)
	}
	return out
}

func tryTool(tools []Tool, name, input string) (string, error) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Run(context.Background(), json.RawMessage(input))
		}
	}
	panic("outil absent : " + name)
}

func wantAll(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("sortie sans %q :\n%s", p, out)
		}
	}
}

func notesFixture(t *testing.T) (*notes.Service, notes.Note) {
	t.Helper()
	svc := &notes.Service{Store: notes.NewFakeStore(), Now: func() time.Time { return testNow }, DefaultScope: tenancy.LocalScope()}
	ctx := context.Background()
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveNote(ctx, n.ID, "Déclaration d'impôts 2026", "fiscal, luxembourg", true); err != nil {
		t.Fatal(err)
	}
	b, err := svc.AddBlock(ctx, n.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveBlock(ctx, n.ID, b.ID, "Formulaire 100 à rendre. "+strings.Repeat("Détail. ", 80), ""); err != nil {
		t.Fatal(err)
	}
	other, _ := svc.NewNote(ctx, "")
	_, _ = svc.SaveNote(ctx, other.ID, "Recette de quiche", "cuisine", false)
	if _, err := svc.AddTask(ctx, "Joindre le certificat de salaire", n.ID, ""); err != nil {
		t.Fatal(err)
	}
	return svc, n
}

func TestNotesTools(t *testing.T) {
	svc, n := notesFixture(t)
	tools := NotesTools(svc)
	out := runTool(t, tools, "search_notes", `{"query":"impôts"}`)
	wantAll(t, out, "1 note", n.ID, "Déclaration d'impôts 2026", "fiscal", "épinglée", "Formulaire 100")
	if strings.Contains(out, "quiche") {
		t.Errorf("recherche non filtrée :\n%s", out)
	}
	// L'extrait d'une liste est court : le texte entier se lit avec read_note.
	if len(out) > 600 {
		t.Errorf("extrait trop long (%d octets)", len(out))
	}
	all := runTool(t, tools, "search_notes", `{}`)
	wantAll(t, all, "2 notes", "quiche")
	if none := runTool(t, tools, "search_notes", `{"query":"zzz"}`); !strings.Contains(none, "Aucune note") {
		t.Errorf("recherche vide = %q", none)
	}

	full := runTool(t, tools, "read_note", `{"id":"`+n.ID+`"}`)
	wantAll(t, full, "Déclaration d'impôts 2026", "Formulaire 100", "Joindre le certificat de salaire", "Tâches liées")
	if strings.Count(full, "Détail.") != 80 {
		t.Errorf("read_note a coupé le texte (%d sur 80)", strings.Count(full, "Détail."))
	}
	if _, err := tryTool(tools, "read_note", `{"id":"absente"}`); err == nil || !strings.Contains(err.Error(), "introuvable") {
		t.Errorf("note absente = %v", err)
	}
	if _, err := tryTool(tools, "read_note", `{}`); err == nil {
		t.Error("read_note sans id accepté")
	}
	for _, tool := range tools {
		if tool.Level != LevelRead || tool.Source != SourceNotes || tool.Description == "" || tool.Schema["type"] != "object" {
			t.Errorf("outil %s mal déclaré : %+v", tool.Name, tool)
		}
	}
}

func TestTaskTools(t *testing.T) {
	svc := &notes.Service{Store: notes.NewFakeStore(), Now: func() time.Time { return testNow }, DefaultScope: tenancy.LocalScope()}
	ctx := context.Background()
	_, _ = svc.CreateTask(ctx, "Payer le loyer", "2026-11-09", "haute") // en retard
	_, _ = svc.CreateTask(ctx, "Appeler Paul", "2026-11-11", "")        // aujourd'hui
	_, _ = svc.CreateTask(ctx, "Réserver le train", "2026-11-14", "")   // cette semaine
	_, _ = svc.CreateTask(ctx, "Préparer Noël", "2026-12-20", "")       // plus tard
	_, _ = svc.CreateTask(ctx, "Ranger la cave", "", "basse")           // sans échéance
	done, _ := svc.CreateTask(ctx, "Déjà faite", "2026-11-10", "")
	_, _ = svc.ToggleTask(ctx, done.ID)
	tools := TaskTools(svc)

	open := runTool(t, tools, "list_tasks", `{}`)
	wantAll(t, open, "5 tâches", "En retard", "Payer le loyer", "2026-11-09", "priorité haute", "Aujourd'hui", "Appeler Paul", "Préparer Noël", "Ranger la cave")
	if strings.Contains(open, "Déjà faite") {
		t.Errorf("une tâche faite est listée parmi les ouvertes :\n%s", open)
	}
	week := runTool(t, tools, "list_tasks", `{"filter":"semaine"}`)
	wantAll(t, week, "3 tâches", "Payer le loyer", "Appeler Paul", "Réserver le train")
	if strings.Contains(week, "Noël") || strings.Contains(week, "cave") {
		t.Errorf("filtre semaine trop large :\n%s", week)
	}
	wantAll(t, runTool(t, tools, "list_tasks", `{"filter":"toutes"}`), "6 tâches", "Déjà faite", "Terminées")
	if _, err := tryTool(tools, "list_tasks", `{"filter":"hier"}`); err == nil {
		t.Error("filtre inconnu accepté")
	}

	out := runTool(t, tools, "add_task", `{"title":"Envoyer le dossier demain","due":"2026-11-20","priority":"haute"}`)
	wantAll(t, out, "Tâche créée", "Envoyer le dossier demain", "2026-11-20")
	tasks, _ := svc.DB(ctx).ListTasks(ctx, notes.TaskQuery{})
	found := false
	for _, task := range tasks {
		if task.Title == "Envoyer le dossier demain" && task.Due == "2026-11-20" && task.Priority == notes.High {
			found = true
		}
	}
	if !found {
		t.Errorf("tâche non créée telle que demandée : %+v", tasks)
	}
	if _, err := tryTool(tools, "add_task", `{"title":"x","due":"vendredi"}`); err == nil || !strings.Contains(err.Error(), "AAAA-MM-JJ") {
		t.Errorf("échéance invalide = %v", err)
	}
	levels := map[string]Level{}
	for _, tool := range tools {
		levels[tool.Name] = tool.Level
	}
	if levels["list_tasks"] != LevelRead || levels["add_task"] != LevelWrite {
		t.Errorf("niveaux = %v", levels)
	}
}

func TestDocumentTools(t *testing.T) {
	store := webapp.NewFakeStore()
	ctx := context.Background()
	merged := `{"numero":{"value":"FAC-2026-0917","confidence":0.98,"source_snippet":"Facture FAC-2026-0917"},
	 "total_ttc":{"value":2317.2,"confidence":0.4,"source_snippet":"2 317,20"},
	 "adresse":{"ville":{"value":"Metz","confidence":1,"source_snippet":"Metz"}}}`
	text := "Atelier Dubois SARL. " + strings.Repeat("ligne de facture ", 1000)
	job, err := store.Create(ctx, webapp.Job{
		ID: "doc-facture", Filename: "facture-dubois.pdf", DocType: "facture", Status: webapp.StatusDone, CreatedAt: testNow,
		Tags: []string{"travaux"}, Comment: "à refacturer", SearchText: text,
		Result: &pipeline.Result{DocType: "facture", SearchText: text,
			Merged: extraction.MergedResult{JSON: json.RawMessage(merged), NeedsReview: true, LowConfidenceFields: []string{"total_ttc"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.Create(ctx, webapp.Job{ID: "doc-passeport", Filename: "passeport.jpg", DocType: "piece_identite", Status: webapp.StatusDone, CreatedAt: testNow.Add(-time.Hour)})
	tools := DocumentTools(webapp.NewJobManager(store, nil))

	out := runTool(t, tools, "search_documents", `{"query":"dubois"}`)
	wantAll(t, out, "1 document", job.ID, "facture-dubois.pdf", "facture", "travaux", "2026-11-11")
	if strings.Contains(out, "passeport") || strings.Contains(out, "ligne de facture") {
		t.Errorf("recherche de documents :\n%s", out)
	}
	wantAll(t, runTool(t, tools, "search_documents", `{}`), "2 documents", "passeport.jpg")

	full := runTool(t, tools, "read_document", `{"id":"`+job.ID+`"}`)
	wantAll(t, full, "facture-dubois.pdf", "à refacturer", "numero = FAC-2026-0917", "total_ttc = 2317.2", "confiance 0.40",
		"à vérifier", "adresse.ville = Metz", "Atelier Dubois SARL", "offset=")
	// Le texte est rendu par tranches, jamais coupé en silence.
	next := runTool(t, tools, "read_document", `{"id":"`+job.ID+`","offset":8000}`)
	if !strings.Contains(next, "ligne de facture") || strings.Contains(next, "Atelier Dubois") || strings.Contains(next, "numero =") {
		t.Errorf("seconde tranche :\n%.300s", next)
	}
	last := runTool(t, tools, "read_document", `{"id":"`+job.ID+`","offset":16000}`)
	if strings.Contains(last, "offset=") {
		t.Errorf("la dernière tranche annonce une suite :\n%.200s", last[len(last)-200:])
	}
	if _, err := tryTool(tools, "read_document", `{"id":"absent"}`); err == nil || !strings.Contains(err.Error(), "introuvable") {
		t.Errorf("document absent = %v", err)
	}
}

func TestMailTools(t *testing.T) {
	store := mail.NewFakeStore()
	ctx := context.Background()
	save := func(m mail.Mail) {
		if _, err := store.Save(ctx, m, nil); err != nil {
			t.Fatal(err)
		}
	}
	save(mail.Mail{ID: "m1", From: mail.Address{Name: "Paul Weber", Email: "paul@example.org"}, To: []mail.Address{{Email: "moi@example.org"}},
		Subject: "Devis toiture", Date: testNow, Text: "Bonjour, peux-tu confirmer le devis ? " + strings.Repeat("blabla ", 2000),
		Attachments: []mail.Attachment{{Index: 0, Filename: "devis.pdf", Size: 1234}},
		Triage:      mail.Triage{Category: mail.Action, Summary: "Paul attend une confirmation du devis.", Reply: true, Question: "Confirmer le devis ?", Version: mail.TriageVersion}})
	save(mail.Mail{ID: "m2", From: mail.Address{Email: "factures@energie.example"}, Subject: "Votre facture", Date: testNow.Add(-time.Hour), Text: "Montant dû 120,00 EUR",
		Triage:  mail.Triage{Category: mail.Document, Summary: "Facture d'électricité.", Version: mail.TriageVersion},
		Payment: mail.Payment{ToPay: true, Payee: "Energie SA", Amount: "120,00", Currency: "EUR", DueDate: "2026-11-30", IBAN: "LU280019400644750000", Version: mail.PaymentVersion}})
	save(mail.Mail{ID: "m3", From: mail.Address{Email: "news@shop.example"}, Subject: "Promotions", Date: testNow.Add(-2 * time.Hour), Text: "Soldes",
		Triage: mail.Triage{Category: mail.Newsletter, Version: mail.TriageVersion}})
	tools := MailTools(func(context.Context) mail.Store { return store })

	all := runTool(t, tools, "search_emails", `{}`)
	wantAll(t, all, "3 emails", "m1", "Paul Weber", "Devis toiture", "Paul attend une confirmation", "réponse attendue", "Promotions")
	if strings.Contains(all, "blabla") {
		t.Errorf("la liste contient le corps des emails")
	}
	reply := runTool(t, tools, "search_emails", `{"filter":"a_repondre"}`)
	wantAll(t, reply, "1 email", "Devis toiture")
	pay := runTool(t, tools, "search_emails", `{"filter":"a_payer"}`)
	wantAll(t, pay, "1 email", "Votre facture", "120,00 EUR", "2026-11-30")
	wantAll(t, runTool(t, tools, "search_emails", `{"query":"toiture"}`), "1 email", "m1")
	if _, err := tryTool(tools, "search_emails", `{"filter":"spam"}`); err == nil {
		t.Error("filtre inconnu accepté")
	}

	one := runTool(t, tools, "read_email", `{"id":"m1"}`)
	wantAll(t, one, "Paul Weber <paul@example.org>", "moi@example.org", "Devis toiture", "Confirmer le devis ?", "devis.pdf", "peux-tu confirmer", "offset=")
	bill := runTool(t, tools, "read_email", `{"id":"m2"}`)
	wantAll(t, bill, "Energie SA", "LU280019400644750000", "120,00 EUR", "Montant dû")
	if _, err := tryTool(tools, "read_email", `{"id":"absent"}`); err == nil {
		t.Error("email absent accepté")
	}
}

func TestMemoryTools(t *testing.T) {
	a, _ := newAssistant(&claudeapi.FakeClient{})
	tools := MemoryTools(a)
	out := runTool(t, tools, "remember", `{"fact":"Le propriétaire s'appelle M. Weber"}`)
	facts, _ := a.Facts(context.Background())
	if len(facts) != 1 || !strings.Contains(out, facts[0].ID) {
		t.Fatalf("remember = %q, faits = %+v", out, facts)
	}
	runTool(t, tools, "forget", `{"id":"`+facts[0].ID+`"}`)
	if facts, _ := a.Facts(context.Background()); len(facts) != 0 {
		t.Errorf("forget n'a rien oublié : %+v", facts)
	}
	if _, err := tryTool(tools, "forget", `{"id":"absent"}`); err == nil {
		t.Error("oubli d'un fait absent accepté")
	}
	for _, tool := range tools {
		if tool.Level != LevelWrite || tool.Source != SourceMemory {
			t.Errorf("outil %s mal déclaré", tool.Name)
		}
	}
}

func TestSelectSources(t *testing.T) {
	tools := []Tool{{Name: "a", Source: SourceNotes}, {Name: "b", Source: SourceEmails}, {Name: "c", Source: SourceMemory}}
	got, err := SelectSources(tools, "notes, memory")
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("SelectSources = %+v, %v", got, err)
	}
	if _, err := SelectSources(tools, "notes,courriel"); err == nil || !strings.Contains(err.Error(), "courriel") {
		t.Errorf("source inconnue = %v", err)
	}
	if got, _ := SelectSources(tools, ""); len(got) != 0 {
		t.Errorf("aucune source = %+v", got)
	}
}

// Deux outils du même nom : l'API refuserait la requête entière.
func TestTools_NamesAreUniqueAndWellFormed(t *testing.T) {
	a, _ := newAssistant(&claudeapi.FakeClient{})
	svc := &notes.Service{Store: notes.NewFakeStore()}
	var all []Tool
	all = append(all, NotesTools(svc)...)
	all = append(all, TaskTools(svc)...)
	all = append(all, DocumentTools(webapp.NewJobManager(webapp.NewFakeStore(), nil))...)
	all = append(all, MailTools(func(context.Context) mail.Store { return mail.NewFakeStore() })...)
	all = append(all, MemoryTools(a)...)
	seen := map[string]bool{}
	for _, tool := range all {
		if seen[tool.Name] {
			t.Errorf("outil en double : %s", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Run == nil || tool.Description == "" || tool.Level == "" || tool.Source == "" || tool.Schema["type"] != "object" {
			t.Errorf("outil %s incomplet", tool.Name)
		}
		if tool.Level == LevelAction {
			t.Errorf("outil %s : aucune action extérieure avant les confirmations (jalon 58)", tool.Name)
		}
		if _, err := json.Marshal(tool.Schema); err != nil {
			t.Errorf("schéma de %s : %v", tool.Name, err)
		}
	}
	if len(all) != 10 {
		t.Errorf("%d outils, 10 attendus", len(all))
	}
}
