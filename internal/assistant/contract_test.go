package assistant

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

type recoverStore interface {
	Store
	Recoverer
}

func userScope(env, user string) tenancy.Scope {
	return tenancy.Scope{Env: tenancy.EnvID(env), User: tenancy.UserID(user), Session: "s", Role: tenancy.RoleMember}
}

// storeContract : FakeStore et MongoStore passent le même contrat. stamp
// rend les identifiants uniques (collections de test partagées).
func storeContract(t *testing.T, base recoverStore, stamp string) {
	t.Helper()
	ctx := context.Background()
	at := func(m int) time.Time { return time.Date(2026, 11, 11, 10, m, 0, 0, time.UTC) }
	id := func(n string) string { return "c-" + stamp + "-" + n }
	s := base.For(userScope("env-"+stamp, "julian"))

	c := Conversation{ID: id("a"), Title: "Impôts", Status: StatusIdle, CreatedAt: at(0), UpdatedAt: at(0)}
	if err := s.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, Conversation{ID: id("b"), Title: "Voyage", Status: StatusIdle, CreatedAt: at(1), UpdatedAt: at(1)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(ctx, id("absent")); ok || err != nil {
		t.Errorf("Get(absent) = %v, %v", ok, err)
	}

	// Le fil est en ajout seul, dans l'ordre, octets du message compris.
	raw := []byte(`{"role":"assistant","content":[{"type":"thinking","signature":"S"}]}`)
	if err := s.Append(ctx, id("a"), at(2), Entry{Role: RoleUser, At: at(2), Text: "Bonjour"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, id("a"), at(3),
		Entry{Role: RoleAssistant, At: at(3), Text: "Je cherche.", Raw: raw, Model: "m",
			Calls: []claudeapi.Call{{ID: "t1", Name: "search_notes", Input: []byte(`{"query":"x"}`)}},
			Usage: claudeapi.Usage{Input: 10, Output: 2}},
		Entry{Role: RoleTools, At: at(3), Results: []claudeapi.Result{{CallID: "t1", Content: "rien", IsError: true}}},
	); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, id("a"))
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if got.Title != "Impôts" || got.Env != tenancy.EnvID("env-"+stamp) || got.User != "julian" || !got.UpdatedAt.Equal(at(3)) || !got.CreatedAt.Equal(at(0)) {
		t.Errorf("conversation = %+v", got)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("entrées = %d", len(got.Entries))
	}
	e := got.Entries[1]
	if got.Entries[0].Text != "Bonjour" || e.Role != RoleAssistant || string(e.Raw) != string(raw) || e.Model != "m" ||
		len(e.Calls) != 1 || e.Calls[0].Name != "search_notes" || string(e.Calls[0].Input) != `{"query":"x"}` ||
		e.Usage != (claudeapi.Usage{Input: 10, Output: 2}) || !e.At.Equal(at(3)) {
		t.Errorf("entrée assistant = %+v", e)
	}
	if r := got.Entries[2].Results; len(r) != 1 || r[0].CallID != "t1" || r[0].Content != "rien" || !r[0].IsError {
		t.Errorf("résultats = %+v", r)
	}
	if err := s.Append(ctx, id("absent"), at(3), Entry{Role: RoleUser}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Append(absent) = %v", err)
	}

	// Liste : la plus récemment modifiée d'abord, sans les entrées.
	list, err := s.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range list {
		ids = append(ids, x.ID)
		if len(x.Entries) != 0 {
			t.Errorf("List a rendu les entrées de %s", x.ID)
		}
	}
	if !reflect.DeepEqual(ids, []string{id("a"), id("b")}) {
		t.Errorf("List = %v", ids)
	}
	if one, _ := s.List(ctx, 1); len(one) != 1 {
		t.Errorf("List(limit 1) = %d", len(one))
	}

	// Start est atomique : sur dix départs simultanés, un seul gagne.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started, err := s.Start(ctx, id("a"), at(4))
			if err != nil {
				t.Error(err)
			}
			if started {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("Start simultanés gagnés = %d, veut 1", wins)
	}
	if got, _, _ := s.Get(ctx, id("a")); got.Status != StatusRunning {
		t.Errorf("status = %q", got.Status)
	}
	if _, err := s.Start(ctx, id("absent"), at(4)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Start(absent) = %v", err)
	}
	if err := s.Finish(ctx, id("a"), "clé refusée", at(5)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, id("a")); got.Status != StatusFailed || got.Error != "clé refusée" || len(got.Entries) != 3 {
		t.Errorf("après Finish(échec) = %+v", got)
	}
	// Un tour en échec se relance ; réussi, l'erreur disparaît.
	if started, _ := s.Start(ctx, id("a"), at(6)); !started {
		t.Error("une conversation en échec doit pouvoir repartir")
	}
	if err := s.Finish(ctx, id("a"), "", at(7)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, id("a")); got.Status != StatusIdle || got.Error != "" {
		t.Errorf("après Finish = %+v", got)
	}
	if err := s.Finish(ctx, id("absent"), "", at(7)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Finish(absent) = %v", err)
	}

	// Reprise au démarrage : tout ce qui est resté « running » échoue.
	if _, err := s.Start(ctx, id("b"), at(8)); err != nil {
		t.Fatal(err)
	}
	n, err := base.FailRunning(ctx, "redémarrage", at(9))
	if err != nil || n < 1 {
		t.Fatalf("FailRunning = %d, %v", n, err)
	}
	if got, _, _ := s.Get(ctx, id("b")); got.Status != StatusFailed || got.Error != "redémarrage" {
		t.Errorf("après FailRunning = %+v", got)
	}
	if got, _, _ := s.Get(ctx, id("a")); got.Status != StatusIdle {
		t.Errorf("FailRunning a touché une conversation au repos : %+v", got)
	}

	// Mémoire.
	if err := s.AddFact(ctx, Fact{ID: id("f1"), Text: "Propriétaire : M. Weber", At: at(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFact(ctx, Fact{ID: id("f2"), Text: "Tutoiement", At: at(2)}); err != nil {
		t.Fatal(err)
	}
	facts, err := s.Facts(ctx)
	if err != nil || len(facts) != 2 || facts[0].Text != "Propriétaire : M. Weber" || facts[1].ID != id("f2") {
		t.Fatalf("Facts = %+v, %v", facts, err)
	}
	if err := s.DeleteFact(ctx, id("f1")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFact(ctx, id("f1")); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteFact(absent) = %v", err)
	}
	if facts, _ := s.Facts(ctx); len(facts) != 1 {
		t.Errorf("Facts après oubli = %+v", facts)
	}

	// Consommation : cumulée par jour.
	_ = s.AddUsage(ctx, "2026-11-11", claudeapi.Usage{Input: 100, Output: 10, CacheRead: 5, CacheWrite: 1})
	_ = s.AddUsage(ctx, "2026-11-11", claudeapi.Usage{Input: 1, Output: 2})
	_ = s.AddUsage(ctx, "2026-11-12", claudeapi.Usage{Input: 7})
	if u, err := s.UsageOn(ctx, "2026-11-11"); err != nil || u != (claudeapi.Usage{Input: 101, Output: 12, CacheRead: 5, CacheWrite: 1}) {
		t.Errorf("UsageOn = %+v, %v", u, err)
	}
	if u, err := s.UsageOn(ctx, "2026-01-01"); err != nil || u.Total() != 0 {
		t.Errorf("UsageOn(jour vide) = %+v, %v", u, err)
	}

	if err := s.Delete(ctx, id("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id("a")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(absent) = %v", err)
	}
	_ = s.Delete(ctx, id("b"))
	_ = s.DeleteFact(ctx, id("f2"))
}

// storeIsolationContract : une conversation et la mémoire sont privées à
// leur user dans leur environnement ; un identifiant d'ailleurs est
// introuvable (jamais « interdit »).
func storeIsolationContract(t *testing.T, base Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	mine := base.For(userScope("iso-"+stamp, "julian"))
	sameEnv := base.For(userScope("iso-"+stamp, "marie"))
	otherEnv := base.For(userScope("iso2-"+stamp, "julian"))
	id := "iso-" + stamp

	if err := mine.Create(ctx, Conversation{ID: id, Title: "privé", Status: StatusIdle, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := mine.AddFact(ctx, Fact{ID: id + "-f", Text: "secret", At: now}); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]Store{"autre user": sameEnv, "autre environnement": otherEnv} {
		if _, ok, err := other.Get(ctx, id); ok || err != nil {
			t.Errorf("%s : Get = %v, %v", name, ok, err)
		}
		if list, _ := other.List(ctx, 0); len(list) != 0 {
			t.Errorf("%s : List = %+v", name, list)
		}
		if err := other.Append(ctx, id, now, Entry{Role: RoleUser, Text: "intrus"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Append = %v", name, err)
		}
		if _, err := other.Start(ctx, id, now); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Start = %v", name, err)
		}
		if err := other.Finish(ctx, id, "x", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Finish = %v", name, err)
		}
		if err := other.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : Delete = %v", name, err)
		}
		if facts, _ := other.Facts(ctx); len(facts) != 0 {
			t.Errorf("%s : Facts = %+v", name, facts)
		}
		if err := other.DeleteFact(ctx, id+"-f"); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s : DeleteFact = %v", name, err)
		}
	}
	if got, ok, _ := mine.Get(ctx, id); !ok || len(got.Entries) != 0 || got.Status != StatusIdle {
		t.Errorf("la conversation a été touchée : %+v", got)
	}
	_ = mine.Delete(ctx, id)
	_ = mine.DeleteFact(ctx, id+"-f")
}

func TestFakeStore_Contract(t *testing.T)  { storeContract(t, NewFakeStore(), "fake") }
func TestFakeStore_Isolation(t *testing.T) { storeIsolationContract(t, NewFakeStore(), "fake") }

// Norme (cf. jalons 43-44) : aucune méthode ne répond sans portée.
func TestStore_RefusesEveryMethodWithoutScope(t *testing.T) {
	s := NewFakeStore().For(tenancy.Scope{})
	ctx := context.Background()
	now := time.Now()
	calls := map[string]func() error{
		"Create":     func() error { return s.Create(ctx, Conversation{ID: "x"}) },
		"Get":        func() error { _, _, err := s.Get(ctx, "x"); return err },
		"List":       func() error { _, err := s.List(ctx, 0); return err },
		"Append":     func() error { return s.Append(ctx, "x", now) },
		"Start":      func() error { _, err := s.Start(ctx, "x", now); return err },
		"Finish":     func() error { return s.Finish(ctx, "x", "", now) },
		"Delete":     func() error { return s.Delete(ctx, "x") },
		"AddFact":    func() error { return s.AddFact(ctx, Fact{ID: "f"}) },
		"Facts":      func() error { _, err := s.Facts(ctx); return err },
		"DeleteFact": func() error { return s.DeleteFact(ctx, "f") },
		"AddUsage":   func() error { return s.AddUsage(ctx, "2026-11-11", claudeapi.Usage{}) },
		"UsageOn":    func() error { _, err := s.UsageOn(ctx, "2026-11-11"); return err },
	}
	typ := reflect.TypeOf((*Store)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if name == "For" {
			continue
		}
		call, ok := calls[name]
		if !ok {
			t.Errorf("méthode %s du Store absente de cette norme : ajoute-la", name)
			continue
		}
		if err := call(); !errors.Is(err, tenancy.ErrNoEnv) {
			t.Errorf("%s sans portée = %v, veut tenancy.ErrNoEnv", name, err)
		}
	}
}
