package trading

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// FakeStore : Store en mémoire, cloisonné par environnement comme
// MongoStore.
type FakeStore struct {
	shared *fakeShared
	scope  tenancy.Scope
}

type fakeShared struct {
	mu      sync.Mutex
	watches map[string]Watch // clé : env + "\x00" + slug
}

// NewFakeStore rend une fake scopée sur tenancy.Local.
func NewFakeStore() *FakeStore {
	return &FakeStore{shared: &fakeShared{watches: map[string]Watch{}}, scope: tenancy.System(tenancy.Local)}
}

func (s *FakeStore) For(scope tenancy.Scope) Store { return &FakeStore{shared: s.shared, scope: scope} }

func (s *FakeStore) key(slug string) string { return string(s.scope.Env) + "\x00" + slug }

func (s *FakeStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("trading: fake store: %w", err)
	}
	return nil
}

func (s *FakeStore) AddWatch(ctx context.Context, w Watch) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.watches[s.key(w.Slug)]; ok {
		return fmt.Errorf("%w : %s", ErrAlreadyWatched, w.Slug)
	}
	w.Env = s.scope.Env
	s.shared.watches[s.key(w.Slug)] = w
	return nil
}

func (s *FakeStore) GetWatch(ctx context.Context, slug string) (Watch, bool, error) {
	if err := s.ensure(); err != nil {
		return Watch{}, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	w, ok := s.shared.watches[s.key(slug)]
	return w, ok, nil
}

func (s *FakeStore) ListWatches(ctx context.Context) ([]Watch, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	var out []Watch
	for _, w := range s.shared.watches {
		if w.Env == s.scope.Env {
			out = append(out, w)
		}
	}
	SortWatches(out)
	return out, nil
}

func (s *FakeStore) SetWatchNote(ctx context.Context, slug, note string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	w, ok := s.shared.watches[s.key(slug)]
	if !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, slug)
	}
	w.Note = note
	s.shared.watches[s.key(slug)] = w
	return nil
}

func (s *FakeStore) RemoveWatch(ctx context.Context, slug string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.watches[s.key(slug)]; !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, slug)
	}
	delete(s.shared.watches, s.key(slug))
	return nil
}

// SortWatches : le plus récemment suivi d'abord, puis par slug (ordre
// stable entre deux affichages).
func SortWatches(ws []Watch) {
	sort.SliceStable(ws, func(i, j int) bool {
		if !ws[i].AddedAt.Equal(ws[j].AddedAt) {
			return ws[i].AddedAt.After(ws[j].AddedAt)
		}
		return ws[i].Slug < ws[j].Slug
	})
}
