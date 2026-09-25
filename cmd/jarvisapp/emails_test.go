package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Emails (jalon 39).

func newMailServer(t *testing.T) (*Server, *mail.FakeStore, *notes.FakeStore) {
	t.Helper()
	s, notesStore := newNotesServer(t)
	store := mail.NewFakeStore()
	ctx := context.Background()
	at := func(h int) time.Time { return notesNow.Add(time.Duration(-h) * time.Hour) }
	store.Save(ctx, mail.Mail{
		ID: "m-facture", Account: "moi@gmail.com", From: mail.Address{Name: "Acme", Email: "compta@acme.fr"},
		To: []mail.Address{{Email: "moi@gmail.com"}}, Subject: "Votre facture n° 42", Date: at(1),
		Text:        "Bonjour,\nci-joint la facture.\n<script>alert(1)</script>",
		Attachments: []mail.Attachment{{Index: 0, Filename: "facture 42.pdf", ContentType: "application/pdf", Size: 5, Stored: true}, {Index: 1, Filename: "énorme.zip", Size: 40 << 20}},
		Triage:      mail.Triage{Category: mail.Action, Summary: "Acme envoie la facture 42 (120 €).", Action: "Payer la facture Acme", Model: "qwen3-8b", Version: mail.TriageVersion},
	}, map[int][]byte{0: []byte("%PDF-")})
	store.Save(ctx, mail.Mail{
		ID: "m-promo", Account: "moi@gmail.com", From: mail.Address{Email: "news@shop.fr"}, Subject: "Soldes d'automne",
		Date: at(2), Seen: true, Text: "-50 %", Triage: mail.Triage{Category: mail.Newsletter, Summary: "Promotions.", Version: mail.TriageVersion},
	}, nil)
	store.Save(ctx, mail.Mail{
		ID: "m-alice", Account: "moi@gmail.com", From: mail.Address{Name: "Alice", Email: "alice@example.com"},
		To: []mail.Address{{Email: "moi@gmail.com"}}, Subject: "Dîner samedi ?", Date: at(4), Text: "Tu es libre samedi soir ?",
		Triage: mail.Triage{Category: mail.Info, Summary: "Alice propose un dîner samedi.", Reply: true, Question: "Dire à Alice si samedi soir convient", Model: "qwen3-8b", Version: mail.TriageVersion},
	}, nil)
	store.Save(ctx, mail.Mail{ID: "m-new", Subject: "Pas encore trié", Date: at(3), Text: "..."}, nil)
	s.Mail = &mail.Service{
		Store: store,
		Syncer: &mail.Syncer{Store: store, Connect: func(ctx context.Context, c mail.Config) (mail.Session, error) {
			return nil, errors.New("AUTHENTICATIONFAILED Invalid credentials")
		}},
		ConfigPath: filepath.Join(t.TempDir(), "mail.json"),
	}
	mail.SaveConfig(s.Mail.ConfigPath, mail.Config{Host: mail.DefaultHost, User: "moi@gmail.com", Password: "secret"})
	return s, store, notesStore
}

func TestEmails_ListShowsTriageAndFilters(t *testing.T) {
	s, _, _ := newMailServer(t)
	page := do(s, http.MethodGet, "/emails?cat=tous", nil).Body.String()
	for _, want := range []string{"Votre facture n° 42", "Acme", "Acme envoie la facture 42 (120 €).", "À traiter", "Soldes d&#39;automne", "Newsletter / promo", "Pas encore trié", "À trier", "moi@gmail.com", `href="/emails/m-facture"`, "📎"} {
		if !strings.Contains(page, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	if i, j := strings.Index(page, "Votre facture"), strings.Index(page, "Soldes"); i < 0 || j < i {
		t.Error("list not newest first")
	}
	filtered := do(s, http.MethodGet, "/emails?cat=a_traiter", nil).Body.String()
	if !strings.Contains(filtered, "Votre facture") || strings.Contains(filtered, "Soldes") {
		t.Error("category filter not applied")
	}
	found := do(s, http.MethodGet, "/emails?q=soldes", nil).Body.String()
	if strings.Contains(found, "Votre facture") || !strings.Contains(found, "Soldes") {
		t.Error("search not applied")
	}
}

// Vue par défaut : seulement les emails qui attendent une réponse ; les
// autres sont masqués (comptés, accessibles par « Tous »), jamais perdus.
func TestEmails_DefaultViewShowsOnlyMailsAwaitingAReply(t *testing.T) {
	s, _, _ := newMailServer(t)
	page := do(s, http.MethodGet, "/emails", nil).Body.String()
	for _, want := range []string{"Dîner samedi ?", "Dire à Alice si samedi soir convient", "À répondre", "2 emails masqués", "1 email en cours d&#39;analyse", `href="/emails?cat=tous"`} {
		if !strings.Contains(page, want) {
			t.Errorf("default view lacks %q", want)
		}
	}
	for _, hidden := range []string{"Votre facture", "Soldes", "Pas encore trié"} {
		if strings.Contains(page, hidden) {
			t.Errorf("default view shows %q, which awaits no reply", hidden)
		}
	}
	all := do(s, http.MethodGet, "/emails?cat=tous", nil).Body.String()
	for _, want := range []string{"Dîner samedi ?", "Votre facture", "Soldes", "Pas encore trié"} {
		if !strings.Contains(all, want) {
			t.Errorf("« Tous » lacks %q", want)
		}
	}
	if strings.Contains(all, "emails masqués") {
		t.Error("« Tous » still says mails are hidden")
	}
	// Une recherche depuis la vue par défaut porte sur toute la boîte.
	if found := do(s, http.MethodGet, "/emails?q=soldes", nil).Body.String(); !strings.Contains(found, "Soldes") {
		t.Error("search from the default view skipped the hidden mails")
	}

	s.Mail.Store.(*mail.FakeStore).SetTriage(context.Background(), "m-alice", mail.Triage{Category: mail.Info, Version: mail.TriageVersion})
	if empty := do(s, http.MethodGet, "/emails", nil).Body.String(); !strings.Contains(empty, "attend de réponse de ta part") {
		t.Error("empty default view not explained")
	}
}

func TestEmails_DetailSaysWhetherAReplyIsExpected(t *testing.T) {
	s, _, _ := newMailServer(t)
	page := do(s, http.MethodGet, "/emails/m-alice", nil).Body.String()
	if !strings.Contains(page, "Réponse attendue") || !strings.Contains(page, "Dire à Alice si samedi soir convient") {
		t.Error("detail does not show the expected reply")
	}
	if page := do(s, http.MethodGet, "/emails/m-promo", nil).Body.String(); !strings.Contains(page, "Pas de réponse attendue") {
		t.Error("detail does not say no reply is expected")
	}
}

func TestEmails_NotConfiguredInvitesToConnect(t *testing.T) {
	s, _, _ := newMailServer(t)
	s.Mail.ConfigPath = filepath.Join(t.TempDir(), "absent.json")
	page := do(s, http.MethodGet, "/emails", nil).Body.String()
	if !strings.Contains(page, `href="/emails/settings"`) || !strings.Contains(page, "Connecter ma boîte") {
		t.Error("no invitation to configure the mailbox")
	}
	if !strings.Contains(page, "Dîner samedi ?") {
		t.Error("mails already stored hidden while the mailbox is not configured")
	}
}

func TestEmails_DetailIsEscapedAndOffersActions(t *testing.T) {
	s, _, _ := newMailServer(t)
	rec := do(s, http.MethodGet, "/emails/m-facture", nil)
	page := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"Votre facture n° 42", "Acme &lt;compta@acme.fr&gt;", "ci-joint la facture.", "&lt;script&gt;alert(1)", "Acme envoie la facture 42", `value="Payer la facture Acme"`, "facture 42.pdf", `/emails/m-facture/attachments/0`, "Envoyer dans Documents", "énorme.zip", "trop volumineuse", "qwen3-8b"} {
		if !strings.Contains(page, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>alert") {
		t.Error("mail body not escaped")
	}
	if do(s, http.MethodGet, "/emails/absent", nil).Code != http.StatusNotFound {
		t.Error("unknown mail: want 404")
	}
}

func TestEmails_TaskAndNoteFromMail(t *testing.T) {
	s, _, notesStore := newMailServer(t)
	rec := do(s, http.MethodPost, "/emails/m-facture/task", url.Values{"input": {"Payer la facture Acme demain"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/emails/m-facture" {
		t.Fatalf("task: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	tasks, _ := notesStore.ListTasks(context.Background(), notes.TaskQuery{MailID: "m-facture"})
	if len(tasks) != 1 || tasks[0].Title != "Payer la facture Acme" || tasks[0].Due == "" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if page := do(s, http.MethodGet, "/emails/m-facture", nil).Body.String(); !strings.Contains(page, "Payer la facture Acme</a>") {
		t.Error("mail page does not list its task")
	}
	if page := do(s, http.MethodGet, "/tasks", nil).Body.String(); !strings.Contains(page, `href="/emails/m-facture"`) || !strings.Contains(page, "📧") {
		t.Error("tasks page does not link the task to its mail")
	}

	rec = do(s, http.MethodPost, "/emails/m-facture/note", nil)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/notes/") {
		t.Fatalf("note: %d %q", rec.Code, loc)
	}
	ns, _ := notesStore.ListNotes(context.Background(), notes.NoteQuery{MailID: "m-facture"})
	if len(ns) != 1 || ns[0].Title != "Votre facture n° 42" || !strings.Contains(ns[0].Body, "Acme envoie la facture 42") || !strings.Contains(ns[0].Body, "> ci-joint la facture.") {
		t.Fatalf("notes = %+v", ns)
	}
	if page := do(s, http.MethodGet, "/notes/"+ns[0].ID, nil).Body.String(); !strings.Contains(page, `href="/emails/m-facture"`) {
		t.Error("note page does not link back to its mail")
	}
	if page := do(s, http.MethodGet, "/emails/m-facture", nil).Body.String(); !strings.Contains(page, `href="/notes/`+ns[0].ID+`"`) {
		t.Error("mail page does not list its note")
	}
	if do(s, http.MethodPost, "/emails/absent/task", url.Values{"input": {"x"}}).Code != http.StatusNotFound {
		t.Error("task on unknown mail: want 404")
	}
}

func TestEmails_AttachmentDownloadAndImport(t *testing.T) {
	s, store, _ := newMailServer(t)
	rec := do(s, http.MethodGet, "/emails/m-facture/attachments/0", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "%PDF-" || rec.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("download: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if do(s, http.MethodGet, "/emails/m-facture/attachments/1", nil).Code != http.StatusNotFound {
		t.Error("attachment not stored: want 404")
	}

	rec = do(s, http.MethodPost, "/emails/m-facture/attachments/0/import", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/emails/m-facture" {
		t.Fatalf("import: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	m, _, _ := store.Get(context.Background(), "m-facture")
	docID := m.Attachments[0].DocID
	job, ok, _ := s.Jobs.Get(context.Background(), docID)
	if !ok || job.Filename != "facture 42.pdf" || strings.Join(job.Tags, ",") != "email" {
		t.Fatalf("job = %+v, %v", job, ok)
	}
	if page := do(s, http.MethodGet, "/emails/m-facture", nil).Body.String(); !strings.Contains(page, `href="/documents/`+docID+`"`) {
		t.Error("mail page does not link the imported document")
	}
	// Une seconde fois : pas de doublon.
	do(s, http.MethodPost, "/emails/m-facture/attachments/0/import", nil)
	jobs, _ := s.Jobs.List(context.Background(), webapp.ListQuery{})
	if len(jobs) != 2 { // le devis de newNotesServer + la facture
		t.Errorf("jobs = %d, want no duplicate import", len(jobs))
	}
	if do(s, http.MethodPost, "/emails/m-facture/attachments/1/import", nil).Code != http.StatusNotFound {
		t.Error("import of an attachment not stored: want 404")
	}
}

func TestEmails_SettingsNeverShowPasswordAndExplainRefusal(t *testing.T) {
	s, _, _ := newMailServer(t)
	page := do(s, http.MethodGet, "/emails/settings", nil).Body.String()
	if !strings.Contains(page, `value="moi@gmail.com"`) || !strings.Contains(page, `type="password"`) || strings.Contains(page, "secret") {
		t.Error("settings: want the address, a password field, never the saved password")
	}
	if !strings.Contains(page, "apppasswords") {
		t.Error("settings: want the link to create a Google app password")
	}
	rec := do(s, http.MethodPost, "/emails/settings", url.Values{"user": {"autre@gmail.com"}, "password": {"faux"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Invalid credentials") || !strings.Contains(rec.Body.String(), `value="autre@gmail.com"`) {
		t.Errorf("refused: %d, want the reason and the input kept", rec.Code)
	}
	if c, _, _ := s.Mail.Config(); c.User != "moi@gmail.com" {
		t.Error("a refused configuration replaced the saved one")
	}
	s.Mail.Syncer.Connect = func(ctx context.Context, c mail.Config) (mail.Session, error) { return okSession{}, nil }
	rec = do(s, http.MethodPost, "/emails/settings", url.Values{"user": {"autre@gmail.com"}, "password": {"abcd efgh ijkl mnop"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/emails" {
		t.Fatalf("accepted: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c, _, _ := s.Mail.Config(); c.User != "autre@gmail.com" || c.Password != "abcdefghijklmnop" {
		t.Errorf("saved = %+v", c)
	}
}

func TestEmails_RetriageAndSync(t *testing.T) {
	s, store, _ := newMailServer(t)
	if rec := do(s, http.MethodPost, "/emails/m-facture/retriage", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("retriage: %d", rec.Code)
	}
	if m, _, _ := store.Get(context.Background(), "m-facture"); m.Triage.Category != "" {
		t.Errorf("triage = %+v, want cleared", m.Triage)
	}
	if rec := do(s, http.MethodPost, "/emails/sync", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/emails" {
		t.Errorf("sync: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestEmails_DisabledWithoutService(t *testing.T) {
	s, _ := newNotesServer(t)
	if rec := do(s, http.MethodGet, "/emails", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

type okSession struct{ mail.Session }

func (okSession) Logout() error { return nil }
