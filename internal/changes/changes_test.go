package changes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func scoped(env tenancy.EnvID, user tenancy.UserID, sess tenancy.SessionID) context.Context {
	return tenancy.WithScope(context.Background(), tenancy.Scope{Env: env, User: user, Session: sess, Role: tenancy.RoleMember})
}

func TestRecorder_CompleteDepuisLaPortee(t *testing.T) {
	j := NewFakeJournal()
	at := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	r := &Recorder{Journal: j, Now: func() time.Time { return at }}

	r.Record(scoped("env-1", "u-7", "s-9"), Op{
		Kind: KindNote, Target: "n1", Label: "Courses", Field: "title",
		Action: Set, Before: JSON("Courses"), After: JSON("Courses de la semaine"),
	})

	ops, err := j.List(context.Background(), Query{Env: "env-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("%d opérations, veut 1", len(ops))
	}
	o := ops[0]
	if o.Env != "env-1" || o.User != "u-7" || o.Session != "s-9" {
		t.Errorf("attribution = %+v : le Recorder doit la remplir depuis la portée", o)
	}
	if !o.At.Equal(at) {
		t.Errorf("At = %v, veut %v", o.At, at)
	}
	if o.ID == "" {
		t.Error("une opération doit avoir un identifiant")
	}
	if o.After != `"Courses de la semaine"` {
		t.Errorf("After = %q", o.After)
	}
}

// Le travail de fond n'est pas une modification d'un humain : le pipeline
// qui écrit un résultat d'extraction n'a rien à faire dans « qui a changé
// quoi ».
func TestRecorder_IgnoreLeTravailDeFond(t *testing.T) {
	j := NewFakeJournal()
	r := &Recorder{Journal: j}
	r.Record(tenancy.WithScope(context.Background(), tenancy.System("env-1")),
		Op{Kind: KindDocument, Target: "d1", Field: "status", Action: Set})
	r.Record(context.Background(), Op{Kind: KindDocument, Target: "d1", Action: Set})

	if ops, _ := j.List(context.Background(), Query{Env: "env-1"}); len(ops) != 0 {
		t.Errorf("%d opérations journalisées, veut 0", len(ops))
	}
}

// Un journal en panne ne doit jamais défaire la modification qu'il décrit.
func TestRecorder_UnJournalEnPanneNeCassePasLOperation(t *testing.T) {
	var logged string
	r := &Recorder{
		Journal: brokenJournal{},
		Logf:    func(f string, a ...any) { logged = f },
	}
	// Ne panique pas, ne rend rien : l'appelant continue.
	r.Record(scoped("env-1", "u-1", "s-1"), Op{Kind: KindNote, Target: "n1", Action: Set})
	if logged == "" {
		t.Error("un échec d'écriture doit être signalé")
	}
}

type brokenJournal struct{}

func (brokenJournal) Append(ctx context.Context, ops ...Op) error {
	return errors.New("base injoignable")
}
func (brokenJournal) List(ctx context.Context, q Query) ([]Op, error) {
	return nil, errors.New("base injoignable")
}

// Un Recorder nil (journal non configuré) ne doit pas faire paniquer les
// services qui l'appellent.
func TestRecorder_Nil(t *testing.T) {
	var r *Recorder
	r.Record(scoped("env-1", "u-1", "s-1"), Op{Kind: KindNote, Target: "n"})
}

func TestJournal_Cloisonne(t *testing.T) {
	j := NewFakeJournal()
	at := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	r := &Recorder{Journal: j, Now: func() time.Time { return at }}
	r.Record(scoped("A", "u-a", "s-a"), Op{Kind: KindNote, Target: "n1", Action: Set})
	r.Record(scoped("B", "u-b", "s-b"), Op{Kind: KindNote, Target: "n2", Action: Set})

	a, _ := j.List(context.Background(), Query{Env: "A"})
	if len(a) != 1 || a[0].Target != "n1" {
		t.Errorf("journal de A = %+v : il ne doit pas voir B", a)
	}
}

func TestJournal_FiltresEtOrdre(t *testing.T) {
	j := NewFakeJournal()
	base := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	for i, o := range []Op{
		{Kind: KindNote, Target: "n1", Field: "title", Action: Set},
		{Kind: KindNote, Target: "n1", Field: "body", Action: Set},
		{Kind: KindDocument, Target: "d1", Field: "tags", Action: Set},
	} {
		at := base.Add(time.Duration(i) * time.Minute)
		(&Recorder{Journal: j, Now: func() time.Time { return at }}).Record(scoped("A", "u-a", "s-a"), o)
	}

	all, _ := j.List(context.Background(), Query{Env: "A"})
	if len(all) != 3 {
		t.Fatalf("%d opérations, veut 3", len(all))
	}
	if all[0].Target != "d1" {
		t.Errorf("la plus récente d'abord, reçu %q", all[0].Target)
	}
	// L'historique d'une entité.
	hist, _ := j.List(context.Background(), Query{Env: "A", Kind: KindNote, Target: "n1"})
	if len(hist) != 2 {
		t.Errorf("%d opérations sur n1, veut 2", len(hist))
	}
	// Mon activité.
	mine, _ := j.List(context.Background(), Query{Env: "A", User: "u-a"})
	if len(mine) != 3 {
		t.Errorf("%d opérations pour u-a, veut 3", len(mine))
	}
	if other, _ := j.List(context.Background(), Query{Env: "A", User: "u-z"}); len(other) != 0 {
		t.Errorf("%d opérations pour un autre user, veut 0", len(other))
	}
	if limited, _ := j.List(context.Background(), Query{Env: "A", Limit: 2}); len(limited) != 2 {
		t.Errorf("%d opérations avec Limit=2", len(limited))
	}
}

func TestJSON(t *testing.T) {
	if got := JSON(nil); got != "" {
		t.Errorf("JSON(nil) = %q, veut vide", got)
	}
	if got := JSON([]string{"a", "b"}); got != `["a","b"]` {
		t.Errorf("JSON = %q", got)
	}
	// Une valeur inencodable ne doit pas faire échouer la journalisation.
	if got := JSON(func() {}); got != "" {
		t.Errorf("JSON(func) = %q, veut vide", got)
	}
}
