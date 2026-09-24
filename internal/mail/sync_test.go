package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/imap"
)

// fakeSession : une boîte IMAP en mémoire.
type fakeSession struct {
	validity uint32
	msgs     map[uint32]imap.Message
	calls    []string
	fetchErr error
}

func (f *fakeSession) Examine(box string) (imap.Mailbox, error) {
	f.calls = append(f.calls, "EXAMINE "+box)
	return imap.Mailbox{UIDValidity: f.validity, Exists: len(f.msgs)}, nil
}

func (f *fakeSession) SearchSince(t time.Time) ([]uint32, error) {
	f.calls = append(f.calls, "SINCE "+t.Format("2006-01-02"))
	var out []uint32
	for uid, m := range f.msgs {
		if !m.InternalDate.Before(t) {
			out = append(out, uid)
		}
	}
	return out, nil
}

func (f *fakeSession) SearchAfter(uid uint32) ([]uint32, error) {
	f.calls = append(f.calls, fmt.Sprintf("AFTER %d", uid))
	var out []uint32
	for u := range f.msgs {
		if u > uid {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeSession) Fetch(uids []uint32) ([]imap.Message, error) {
	f.calls = append(f.calls, fmt.Sprint("FETCH ", uids))
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	var out []imap.Message
	for _, u := range uids {
		out = append(out, f.msgs[u])
	}
	return out, nil
}

func (f *fakeSession) Logout() error { f.calls = append(f.calls, "LOGOUT"); return nil }

// rawMail : sans en-tête Date, la date de réception (INTERNALDATE) fait foi.
func rawMail(subject, msgID, body string) []byte {
	return []byte("From: Alice <alice@example.com>\r\nTo: moi@gmail.com\r\nSubject: " + subject +
		"\r\nMessage-ID: " + msgID + "\r\n\r\n" + body + "\r\n")
}

var now = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func newSyncer(store Store, sess *fakeSession) *Syncer {
	return &Syncer{
		Store: store, Days: 30, Batch: 2, Now: func() time.Time { return now },
		Connect: func(ctx context.Context, cfg Config) (Session, error) { return sess, nil },
	}
}

var cfg = Config{Host: "imap.gmail.com:993", User: "moi@gmail.com", Password: "secret"}

func TestSyncer_FirstSyncFetchesRecentMailInBatches(t *testing.T) {
	sess := &fakeSession{validity: 7, msgs: map[uint32]imap.Message{
		10: {UID: 10, InternalDate: now.AddDate(0, 0, -60), Raw: rawMail("Ancien", "<a@x>", "vieux")},
		20: {UID: 20, InternalDate: now.AddDate(0, 0, -3), Raw: rawMail("Facture", "<b@x>", "à payer"), Flags: []string{`\Seen`}},
		21: {UID: 21, InternalDate: now.AddDate(0, 0, -2), Raw: rawMail("Réunion", "<c@x>", "jeudi")},
		22: {UID: 22, InternalDate: now.AddDate(0, 0, -1), Raw: rawMail("Promo", "<d@x>", "-50 %")},
	}}
	store := NewFakeStore()
	rep, err := newSyncer(store, sess).Sync(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.New != 3 {
		t.Errorf("New = %d, want 3 (the 60-day-old mail is outside the window)", rep.New)
	}
	want := "EXAMINE INBOX|SINCE 2026-08-25|FETCH [20 21]|FETCH [22]|LOGOUT"
	if got := strings.Join(sess.calls, "|"); got != want {
		t.Errorf("calls = %s\nwant    %s", got, want)
	}
	ms, _ := store.List(context.Background(), Query{})
	if len(ms) != 3 || ms[0].Subject != "Promo" || ms[2].Subject != "Facture" || !ms[2].Seen || ms[0].Seen {
		t.Fatalf("stored = %+v", ms)
	}
	if m := ms[2]; m.Account != "moi@gmail.com" || m.UID != 20 || m.UIDValidity != 7 || m.From.Email != "alice@example.com" || m.MessageID != "<b@x>" || !strings.Contains(m.Text, "à payer") {
		t.Errorf("mail = %+v", m)
	}
}

func TestSyncer_NextSyncOnlyAsksForNewUIDs(t *testing.T) {
	sess := &fakeSession{validity: 7, msgs: map[uint32]imap.Message{
		20: {UID: 20, InternalDate: now.AddDate(0, 0, -3), Raw: rawMail("Facture", "<b@x>", "à payer")},
	}}
	store := NewFakeStore()
	s := newSyncer(store, sess)
	if _, err := s.Sync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sess.msgs[23] = imap.Message{UID: 23, InternalDate: now, Raw: rawMail("Nouveau", "<e@x>", "salut")}
	sess.calls = nil
	rep, err := s.Sync(context.Background(), cfg)
	if err != nil || rep.New != 1 {
		t.Fatalf("Sync = %+v, %v", rep, err)
	}
	if got := strings.Join(sess.calls, "|"); got != "EXAMINE INBOX|AFTER 20|FETCH [23]|LOGOUT" {
		t.Errorf("calls = %s", got)
	}
}

// Une UIDVALIDITY nouvelle : les UID ne veulent plus rien dire, on repart
// de la fenêtre de dates — sans doublon grâce au Message-ID.
func TestSyncer_NewUIDValidityRestartsFromDateWithoutDuplicates(t *testing.T) {
	sess := &fakeSession{validity: 7, msgs: map[uint32]imap.Message{
		20: {UID: 20, InternalDate: now.AddDate(0, 0, -3), Raw: rawMail("Facture", "<b@x>", "à payer")},
	}}
	store := NewFakeStore()
	s := newSyncer(store, sess)
	s.Sync(context.Background(), cfg)
	sess.validity = 8
	sess.msgs = map[uint32]imap.Message{3: {UID: 3, InternalDate: now.AddDate(0, 0, -3), Raw: rawMail("Facture", "<b@x>", "à payer")}}
	sess.calls = nil
	rep, err := s.Sync(context.Background(), cfg)
	if err != nil || rep.New != 0 {
		t.Errorf("Sync = %+v, %v, want 0 new (same Message-ID)", rep, err)
	}
	if !strings.Contains(strings.Join(sess.calls, "|"), "SINCE") {
		t.Errorf("calls = %v, want a date search", sess.calls)
	}
}

func TestSyncer_KeepsAttachmentsAndUnreadableMail(t *testing.T) {
	raw := "From: compta@acme.fr\r\nSubject: Votre facture\r\nMessage-ID: <f@x>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Bonjour,<br>ci-joint <b>la facture</b>.</p>\r\n" +
		"--b\r\nContent-Type: application/pdf; name=\"f.pdf\"\r\nContent-Disposition: attachment; filename=\"f.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0=\r\n--b--\r\n"
	sess := &fakeSession{validity: 7, msgs: map[uint32]imap.Message{
		30: {UID: 30, InternalDate: now, Raw: []byte(raw)},
		31: {UID: 31, InternalDate: now, Raw: []byte("\r\n\r\n")},
	}}
	store := NewFakeStore()
	if _, err := newSyncer(store, sess).Sync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	ms, _ := store.List(context.Background(), Query{})
	if len(ms) != 2 {
		t.Fatalf("stored %d mails, want 2 (an unreadable one is kept, not lost)", len(ms))
	}
	var withPDF, broken Mail
	for _, m := range ms {
		if m.UID == 30 {
			withPDF = m
		} else {
			broken = m
		}
	}
	if !strings.Contains(withPDF.Text, "ci-joint la facture") || strings.Contains(withPDF.Text, "<b>") {
		t.Errorf("Text = %q, want the HTML body as text", withPDF.Text)
	}
	if len(withPDF.Attachments) != 1 || withPDF.Attachments[0].Filename != "f.pdf" || !withPDF.Attachments[0].Stored {
		t.Fatalf("attachments = %+v", withPDF.Attachments)
	}
	if data, ok, _ := store.Attachment(context.Background(), withPDF.ID, 0); !ok || string(data) != "%PDF-" {
		t.Errorf("attachment data = %q", data)
	}
	if broken.Subject != "(email illisible)" || broken.Text == "" {
		t.Errorf("broken = %+v", broken)
	}
}

func TestSyncer_ErrorsAreReported(t *testing.T) {
	store := NewFakeStore()
	s := newSyncer(store, nil)
	s.Connect = func(ctx context.Context, cfg Config) (Session, error) { return nil, errors.New("connexion refusée") }
	if _, err := s.Sync(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "connexion refusée") {
		t.Errorf("Sync = %v", err)
	}
	sess := &fakeSession{validity: 7, fetchErr: errors.New("coupure"), msgs: map[uint32]imap.Message{1: {UID: 1, InternalDate: now}}}
	s = newSyncer(store, sess)
	if _, err := s.Sync(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "coupure") {
		t.Errorf("Sync = %v", err)
	}
	if _, err := s.Sync(context.Background(), Config{}); err == nil {
		t.Error("Sync without configuration = nil")
	}
}

func TestMailID_StableAndPerAccount(t *testing.T) {
	a := mailID("moi@gmail.com", "<b@x>", 7, 20)
	if a != mailID("moi@gmail.com", "<b@x>", 9, 3) {
		t.Error("same Message-ID, different UID: different IDs")
	}
	if a == mailID("autre@gmail.com", "<b@x>", 7, 20) {
		t.Error("two accounts share an ID")
	}
	if mailID("moi@gmail.com", "", 7, 20) == mailID("moi@gmail.com", "", 7, 21) {
		t.Error("without Message-ID, the UID must tell mails apart")
	}
}
