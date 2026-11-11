package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
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
		wantLevel := LevelRead
		if tool.Name == "create_note" || tool.Name == "append_to_note" {
			wantLevel = LevelWrite
		}
		if tool.Level != wantLevel || tool.Source != SourceNotes || tool.Description == "" || tool.Schema["type"] != "object" {
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
	svc := &mail.Service{Store: store, DefaultScope: tenancy.LocalScope()}
	tools := MailTools(svc)

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
	all = append(all, MailTools(&mail.Service{Store: mail.NewFakeStore()})...)
	all = append(all, TicketTools(&fakeTickets{})...)
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
		// Une action dit toujours ce qu'elle va faire avant d'être confirmée.
		if (tool.Level == LevelAction) != (tool.Confirm != nil) {
			t.Errorf("outil %s : Confirm est attendu pour une action, et seulement pour elle", tool.Name)
		}
		if _, err := json.Marshal(tool.Schema); err != nil {
			t.Errorf("schéma de %s : %v", tool.Name, err)
		}
	}
	if len(all) != 19 {
		t.Errorf("%d outils, 19 attendus", len(all))
	}
}

func TestNotesTools_Write(t *testing.T) {
	svc, n := notesFixture(t)
	tools := NotesTools(svc)
	ctx := context.Background()

	out := runTool(t, tools, "create_note", `{"title":"Compte rendu notaire","text":"Signature le 3 décembre.\n\nApporter la pièce d'identité.","tags":["immobilier","notaire"]}`)
	wantAll(t, out, "Note créée", "Compte rendu notaire")
	list, _ := svc.DB(ctx).ListNotes(ctx, notes.NoteQuery{Search: "notaire"})
	if len(list) != 1 || list[0].Title != "Compte rendu notaire" || !strings.Contains(list[0].Body, "Signature le 3 décembre.") ||
		len(list[0].Tags) != 2 || !strings.Contains(out, list[0].ID) {
		t.Fatalf("note créée = %+v", list)
	}
	// Une seule boîte, pas une boîte vide suivie du texte.
	if blocks := notes.BlocksOf(list[0]); len(blocks) != 1 {
		t.Errorf("boîtes = %d, veut 1", len(blocks))
	}
	if _, err := tryTool(tools, "create_note", `{"title":" ","text":"x"}`); err == nil {
		t.Error("note sans titre acceptée")
	}

	before, _, _ := svc.DB(ctx).GetNote(ctx, n.ID)
	runTool(t, tools, "append_to_note", `{"id":"`+n.ID+`","text":"Reçu le certificat le 12/11."}`)
	after, _, _ := svc.DB(ctx).GetNote(ctx, n.ID)
	if len(notes.BlocksOf(after)) != len(notes.BlocksOf(before))+1 || !strings.HasSuffix(strings.TrimSpace(after.Body), "Reçu le certificat le 12/11.") ||
		!strings.Contains(after.Body, "Formulaire 100") {
		t.Errorf("append_to_note : %q", after.Body)
	}
	if _, err := tryTool(tools, "append_to_note", `{"id":"absente","text":"x"}`); err == nil {
		t.Error("ajout à une note absente accepté")
	}
	if _, err := tryTool(tools, "append_to_note", `{"id":"`+n.ID+`","text":"  "}`); err == nil {
		t.Error("ajout vide accepté")
	}
}

func TestTaskTools_Write(t *testing.T) {
	svc := &notes.Service{Store: notes.NewFakeStore(), Now: func() time.Time { return testNow }, DefaultScope: tenancy.LocalScope()}
	ctx := context.Background()
	task, _ := svc.CreateTask(ctx, "Payer le loyer", "2026-11-09", "haute")
	tools := TaskTools(svc)
	get := func() notes.Task { got, _, _ := svc.DB(ctx).GetTask(ctx, task.ID); return got }

	// Ne changer que ce qui est donné.
	runTool(t, tools, "update_task", `{"id":"`+task.ID+`","due":"2026-11-20"}`)
	if got := get(); got.Due != "2026-11-20" || got.Title != "Payer le loyer" || got.Priority != notes.High {
		t.Errorf("update_task(due) = %+v", got)
	}
	runTool(t, tools, "update_task", `{"id":"`+task.ID+`","title":"Payer le loyer de novembre","due":"","priority":""}`)
	if got := get(); got.Title != "Payer le loyer de novembre" || got.Due != "" || got.Priority != notes.Normal {
		t.Errorf("update_task(tout) = %+v", got)
	}
	if _, err := tryTool(tools, "update_task", `{"id":"`+task.ID+`","due":"demain"}`); err == nil {
		t.Error("échéance invalide acceptée")
	}

	wantAll(t, runTool(t, tools, "complete_task", `{"id":"`+task.ID+`"}`), "terminée")
	if !get().Done {
		t.Fatal("tâche non terminée")
	}
	// Terminer deux fois ne la rouvre pas.
	runTool(t, tools, "complete_task", `{"id":"`+task.ID+`"}`)
	if !get().Done {
		t.Error("complete_task a rouvert une tâche déjà faite")
	}

	var del Tool
	for _, tool := range tools {
		if tool.Name == "delete_task" {
			del = tool
		}
	}
	if del.Level != LevelAction {
		t.Fatalf("delete_task = %q, veut une action à confirmer", del.Level)
	}
	if s := del.Confirm(ctx, json.RawMessage(`{"id":"`+task.ID+`"}`)); !strings.Contains(s, "Payer le loyer de novembre") {
		t.Errorf("confirmation = %q : elle doit nommer la tâche", s)
	}
	runTool(t, tools, "delete_task", `{"id":"`+task.ID+`"}`)
	if _, ok, _ := svc.DB(ctx).GetTask(ctx, task.ID); ok {
		t.Error("tâche non supprimée")
	}
	if _, err := tryTool(tools, "complete_task", `{"id":"absente"}`); err == nil {
		t.Error("tâche absente acceptée")
	}
}

func TestDocumentTools_SetTags(t *testing.T) {
	store := webapp.NewFakeStore()
	ctx := context.Background()
	job, _ := store.Create(ctx, webapp.Job{ID: "d1", Filename: "f.pdf", Status: webapp.StatusDone, CreatedAt: testNow, Tags: []string{"ancien"}})
	tools := DocumentTools(webapp.NewJobManager(store, nil))
	wantAll(t, runTool(t, tools, "set_document_tags", `{"id":"`+job.ID+`","tags":[" travaux ","2026","travaux",""]}`), "travaux", "2026")
	got, _, _ := store.Get(ctx, job.ID)
	if strings.Join(got.Tags, ",") != "travaux,2026" {
		t.Errorf("tags = %v", got.Tags)
	}
	if _, err := tryTool(tools, "set_document_tags", `{"id":"absent","tags":["x"]}`); err == nil {
		t.Error("document absent accepté")
	}
}

func TestMailTools_Write(t *testing.T) {
	store := mail.NewFakeStore()
	ctx := context.Background()
	_, _ = store.Save(ctx, mail.Mail{ID: "m1", Subject: "Facture", Date: testNow, Payment: mail.Payment{ToPay: true, Payee: "Energie SA"}}, nil)
	tools := MailTools(&mail.Service{Store: store, DefaultScope: tenancy.LocalScope()})
	runTool(t, tools, "mark_email_paid", `{"id":"m1"}`)
	runTool(t, tools, "archive_email", `{"id":"m1"}`)
	m, _, _ := store.Get(ctx, "m1")
	if !m.Paid || !m.Archived {
		t.Errorf("email = payé %v, archivé %v", m.Paid, m.Archived)
	}
	for _, name := range []string{"mark_email_paid", "archive_email"} {
		if _, err := tryTool(tools, name, `{"id":"absent"}`); err == nil {
			t.Errorf("%s sur un email absent accepté", name)
		}
	}
}

type fakeTickets struct{ created []string }

func (f *fakeTickets) Create(ctx context.Context, title, need, acceptance string) (tickets.Ticket, error) {
	if strings.TrimSpace(title) == "" {
		return tickets.Ticket{}, errors.New("titre vide")
	}
	f.created = append(f.created, title+"|"+need+"|"+acceptance)
	return tickets.Ticket{ID: "tk1", Title: title}, nil
}

// Un ticket fait écrire du code et le déploie : c'est une action, à
// confirmer, et réservée au propriétaire de l'instance.
func TestTicketTools(t *testing.T) {
	f := &fakeTickets{}
	tools := TicketTools(f)
	if len(tools) != 1 || tools[0].Level != LevelAction || tools[0].Source != SourceTickets {
		t.Fatalf("outils = %+v", tools)
	}
	in := json.RawMessage(`{"title":"Export CSV des tâches","need":"Pouvoir exporter la todo.","acceptance":"Un bouton Exporter sur la page Tâches"}`)
	if s := tools[0].Confirm(context.Background(), in); !strings.Contains(s, "Export CSV des tâches") || !strings.Contains(s, "déplo") {
		t.Errorf("confirmation = %q : elle doit nommer le ticket et dire qu'il peut être déployé", s)
	}
	owner := tenancy.WithScope(context.Background(), tenancy.LocalScope())
	out, err := tools[0].Run(owner, in)
	if err != nil || !strings.Contains(out, "tk1") || len(f.created) != 1 ||
		f.created[0] != "Export CSV des tâches|Pouvoir exporter la todo.|Un bouton Exporter sur la page Tâches" {
		t.Fatalf("create_ticket = %q, %v, %v", out, err, f.created)
	}
	member := tenancy.WithScope(context.Background(), userScope("famille", "marie"))
	if _, err := tools[0].Run(member, in); err == nil || len(f.created) != 1 {
		t.Errorf("un membre a créé un ticket : %v", err)
	}
	if _, err := tools[0].Run(context.Background(), in); err == nil {
		t.Error("ticket créé sans portée")
	}
}
