package changes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// fakeApplier enregistre ce qu'on lui donne et rend tout appliqué, sauf
// panne ou conflit configurés.
type fakeApplier struct {
	mu       sync.Mutex
	got      []Op
	err      error
	conflict bool
}

func (a *fakeApplier) Kinds() []Kind { return []Kind{KindNote} }

func (a *fakeApplier) Apply(ctx context.Context, scope tenancy.Scope, ops []Op) ([]Op, []Conflict, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, nil, a.err
	}
	a.got = append(a.got, ops...)
	if a.conflict {
		var cs []Conflict
		for _, op := range ops {
			cs = append(cs, Conflict{Op: op, Reason: "modifié entre-temps"})
		}
		return nil, cs, nil
	}
	return ops, nil, nil
}

func titleOp() Op {
	return Op{Kind: KindNote, Target: "n1", Field: "title", Action: Set,
		Before: JSON("a"), After: JSON("b")}
}

func pendingOf(t *testing.T, s ChangesetStore) []Op {
	t.Helper()
	sc := tenancy.LocalScope()
	ops, err := s.Pending(context.Background(), sc.Env, sc.User)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func localCtx() context.Context {
	return tenancy.WithScope(context.Background(), tenancy.LocalScope())
}

func TestStager_AutoCommit_AppliqueAussitot(t *testing.T) {
	store := NewFakeChangesetStore()
	app := &fakeApplier{}
	s := &Stager{Store: store, AutoCommit: &Committer{Staged: store, Appliers: []Applier{app}}}

	if err := s.Stage(localCtx(), titleOp()); err != nil {
		t.Fatal(err)
	}
	if len(app.got) != 1 {
		t.Fatalf("l'Applier a reçu %d opérations, veut 1 : le commit automatique doit appliquer aussitôt", len(app.got))
	}
	if ops := pendingOf(t, store); len(ops) != 0 {
		t.Errorf("%d opérations encore en attente, veut 0", len(ops))
	}
}

func TestStager_AutoCommit_ConflitResteEnAttente(t *testing.T) {
	store := NewFakeChangesetStore()
	app := &fakeApplier{conflict: true}
	var logs []string
	s := &Stager{Store: store, AutoCommit: &Committer{Staged: store, Appliers: []Applier{app}},
		Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}

	if err := s.Stage(localCtx(), titleOp()); err != nil {
		t.Fatalf("un conflit n'est pas une erreur d'écriture : %v", err)
	}
	if ops := pendingOf(t, store); len(ops) != 1 {
		t.Fatalf("%d opérations en attente, veut 1 : un conflit n'est jamais appliqué ni perdu", len(ops))
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "conflit") {
		t.Errorf("journal = %q, veut une mention du conflit", logs)
	}
}

func TestStager_AutoCommit_PanneNeFaitPasEchouer(t *testing.T) {
	store := NewFakeChangesetStore()
	app := &fakeApplier{err: errors.New("Atlas injoignable")}
	var logs []string
	s := &Stager{Store: store, AutoCommit: &Committer{Staged: store, Appliers: []Applier{app}},
		Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}

	if err := s.Stage(localCtx(), titleOp()); err != nil {
		t.Fatalf("la modification est enregistrée : la panne du commit ne doit pas remonter (%v)", err)
	}
	if ops := pendingOf(t, store); len(ops) != 1 {
		t.Errorf("%d opérations en attente, veut 1 : rien ne doit être perdu", len(ops))
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "commit automatique") || !strings.Contains(logs[0], "Atlas injoignable") {
		t.Errorf("journal = %q, veut l'échec du commit automatique et sa cause", logs)
	}
}

func TestStager_SansAutoCommit_ResteEnAttente(t *testing.T) {
	store := NewFakeChangesetStore()
	s := &Stager{Store: store}
	if err := s.Stage(localCtx(), titleOp()); err != nil {
		t.Fatal(err)
	}
	if ops := pendingOf(t, store); len(ops) != 1 {
		t.Errorf("%d opérations en attente, veut 1", len(ops))
	}
}

type failingStage struct{ *FakeChangesetStore }

func (failingStage) Stage(ctx context.Context, ops ...Op) error {
	return errors.New("écriture refusée")
}

func TestStager_AutoCommit_EchecDeMiseEnAttente(t *testing.T) {
	store := failingStage{NewFakeChangesetStore()}
	app := &fakeApplier{}
	s := &Stager{Store: store, AutoCommit: &Committer{Staged: store, Appliers: []Applier{app}}}

	if err := s.Stage(localCtx(), titleOp()); err == nil {
		t.Fatal("une mise en attente ratée doit remonter")
	}
	if len(app.got) != 0 {
		t.Error("rien à commiter quand la mise en attente a échoué")
	}
}
