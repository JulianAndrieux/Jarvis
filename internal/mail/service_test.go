package mail

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/imap"
)

func newService(t *testing.T, sess *fakeSession, model *scriptedLLM) (*Service, *FakeStore) {
	t.Helper()
	store := NewFakeStore()
	s := &Service{
		Store:      store,
		Syncer:     newSyncer(store, sess),
		Triager:    &Triager{LLM: model, Model: "qwen3-8b", Now: func() time.Time { return now }},
		ConfigPath: filepath.Join(t.TempDir(), "mail.json"),
		Now:        func() time.Time { return now },
	}
	return s, store
}

func oneMailbox() *fakeSession {
	return &fakeSession{validity: 7, msgs: map[uint32]imap.Message{
		20: {UID: 20, InternalDate: now.AddDate(0, 0, -1), Raw: rawMail("Facture", "<b@x>", "à payer")},
		21: {UID: 21, InternalDate: now, Raw: rawMail("Promo", "<c@x>", "-50 %")},
	}}
}

func TestService_NotConfiguredDoesNothing(t *testing.T) {
	sess := oneMailbox()
	s, _ := newService(t, sess, &scriptedLLM{})
	s.Cycle(context.Background())
	if len(sess.calls) != 0 {
		t.Errorf("calls = %v, want no connection without configuration", sess.calls)
	}
	if st := s.Status(); st.Configured || st.LastError != "" {
		t.Errorf("Status = %+v", st)
	}
}

func TestService_ConfigureTestsTheConnectionFirst(t *testing.T) {
	sess := oneMailbox()
	s, _ := newService(t, sess, &scriptedLLM{})
	var tried Config
	s.Syncer.Connect = func(ctx context.Context, c Config) (Session, error) {
		tried = c
		return nil, errors.New("AUTHENTICATIONFAILED Invalid credentials")
	}
	err := s.Configure(context.Background(), Config{User: "moi@gmail.com", Password: "abcd efgh ijkl mnop"})
	if err == nil || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Fatalf("Configure = %v, want the server's refusal", err)
	}
	if tried.Host != DefaultHost || tried.Password != "abcdefghijklmnop" {
		t.Errorf("tried = %+v, want the normalized configuration", tried)
	}
	if _, ok, _ := s.Config(); ok {
		t.Error("a refused configuration was saved")
	}
	if err := s.Configure(context.Background(), Config{User: "moi@gmail.com"}); err == nil {
		t.Error("Configure without password = nil")
	}

	s.Syncer.Connect = func(ctx context.Context, c Config) (Session, error) { return sess, nil }
	if err := s.Configure(context.Background(), Config{User: "moi@gmail.com", Password: "x"}); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := s.Config(); !ok || c.User != "moi@gmail.com" {
		t.Errorf("Config = %+v, %v", c, ok)
	}
	if st := s.Status(); !st.Configured || st.Account != "moi@gmail.com" {
		t.Errorf("Status = %+v", st)
	}
}

func TestService_CycleSyncsThenTriagesThroughTheModelQueue(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"a_traiter","summary":"Une facture à payer.","action":"Payer la facture"}`}
	s, store := newService(t, oneMailbox(), model)
	SaveConfig(s.ConfigPath, cfg)
	var acquired, released int
	s.Acquire = func(ctx context.Context) (func(), error) {
		acquired++
		return func() { released++ }, nil
	}
	s.Cycle(context.Background())

	st := s.Status()
	if st.LastError != "" || st.LastNew != 2 || !st.LastSync.Equal(now) || st.Running {
		t.Errorf("Status = %+v", st)
	}
	ms, _ := store.List(context.Background(), Query{})
	for _, m := range ms {
		if m.Triage.Category != Action || m.Triage.Action != "Payer la facture" {
			t.Errorf("%s triage = %+v", m.Subject, m.Triage)
		}
	}
	if acquired != 2 || released != 2 {
		t.Errorf("model queue acquired %d, released %d, want once per mail", acquired, released)
	}
}

// Une réponse inutilisable est notée sur l'email (pas retentée en boucle) ;
// un modèle injoignable arrête le tri sans rien noter (retenté plus tard).
func TestService_TriageFailures(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"spam","summary":"","action":""}`}
	s, store := newService(t, oneMailbox(), model)
	SaveConfig(s.ConfigPath, cfg)
	s.Cycle(context.Background())
	ms, _ := store.List(context.Background(), Query{})
	for _, m := range ms {
		if m.Triage.Error == "" {
			t.Errorf("%s: want the bad reply noted", m.Subject)
		}
	}
	if pending, _ := store.List(context.Background(), Query{Untriaged: true}); len(pending) != 0 {
		t.Errorf("pending = %d, want a noted bad reply not retried", len(pending))
	}

	s2, store2 := newService(t, oneMailbox(), &scriptedLLM{err: errors.New("connection refused")})
	SaveConfig(s2.ConfigPath, cfg)
	s2.Cycle(context.Background())
	if pending, _ := store2.List(context.Background(), Query{Untriaged: true}); len(pending) != 2 {
		t.Errorf("pending = %d, want both left for later", len(pending))
	}
	if st := s2.Status(); !strings.Contains(st.LastError, "connection refused") {
		t.Errorf("Status = %+v", st)
	}

	s3, store3 := newService(t, oneMailbox(), model)
	SaveConfig(s3.ConfigPath, cfg)
	s3.Acquire = func(ctx context.Context) (func(), error) { return nil, errors.New("modèles indisponibles") }
	s3.Cycle(context.Background())
	if pending, _ := store3.List(context.Background(), Query{Untriaged: true}); len(pending) != 2 {
		t.Errorf("pending = %d, want both left for later", len(pending))
	}
}

func TestService_SyncErrorIsShownAndTriageStillRuns(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"information","summary":"s","action":""}`}
	s, store := newService(t, oneMailbox(), model)
	SaveConfig(s.ConfigPath, cfg)
	store.Save(context.Background(), Mail{ID: "old", Subject: "Déjà relevé"}, nil)
	s.Syncer.Connect = func(ctx context.Context, c Config) (Session, error) { return nil, errors.New("réseau coupé") }
	s.Cycle(context.Background())
	if st := s.Status(); !strings.Contains(st.LastError, "réseau coupé") {
		t.Errorf("Status = %+v", st)
	}
	if m, _, _ := store.Get(context.Background(), "old"); m.Triage.Category != Info {
		t.Errorf("triage = %+v, want mail already stored triaged anyway", m.Triage)
	}
}

func TestService_RetriageAndRunLoop(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"information","summary":"s","action":""}`}
	s, store := newService(t, oneMailbox(), model)
	SaveConfig(s.ConfigPath, cfg)
	s.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, func() bool {
		p, _ := store.List(ctx, Query{Untriaged: true})
		ms, _ := store.List(ctx, Query{})
		return len(ms) == 2 && len(p) == 0
	})

	ms, _ := store.List(ctx, Query{})
	model.reply = `{"category":"newsletter","summary":"promo","action":""}`
	if err := s.Retriage(ctx, ms[0].ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { m, _, _ := store.Get(ctx, ms[0].ID); return m.Triage.Category == Newsletter })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

// Un lot plein : le cycle suivant part sans attendre l'intervalle.
func TestService_FullTriageBatchTriggersAnotherCycle(t *testing.T) {
	model := &scriptedLLM{reply: `{"category":"information","summary":"s","action":""}`}
	s, store := newService(t, oneMailbox(), model)
	SaveConfig(s.ConfigPath, cfg)
	s.TriageBatch = 1
	s.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitFor(t, func() bool {
		ms, _ := store.List(ctx, Query{})
		p, _ := store.List(ctx, Query{Untriaged: true})
		return len(ms) == 2 && len(p) == 0
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Le tri attend son tour dans la file des modèles (un ticket peut la tenir
// une heure) : la relève, elle, continue.
func TestService_SyncDoesNotWaitForTheModelQueue(t *testing.T) {
	sess := oneMailbox()
	model := &scriptedLLM{reply: `{"category":"information","summary":"s","action":""}`}
	s, store := newService(t, sess, model)
	SaveConfig(s.ConfigPath, cfg)
	s.Interval = time.Hour
	blocked := make(chan struct{})
	s.Acquire = func(ctx context.Context) (func(), error) {
		select {
		case <-blocked:
			return func() {}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitFor(t, func() bool { ms, _ := store.List(ctx, Query{}); return len(ms) == 2 })
	sess.msgs[22] = imap.Message{UID: 22, InternalDate: now, Raw: rawMail("Pendant le ticket", "<z@x>", "...")}
	s.Kick()
	waitFor(t, func() bool { ms, _ := store.List(ctx, Query{}); return len(ms) == 3 })
	close(blocked)
	waitFor(t, func() bool { p, _ := store.List(ctx, Query{Untriaged: true}); return len(p) == 0 })
}
