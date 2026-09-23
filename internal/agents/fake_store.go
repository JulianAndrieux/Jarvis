package agents

import (
	"context"
	"sync"
)

// FakeStore est un Store en mémoire pour les tests. Err, s'il est
// renseigné, fait échouer Save.
type FakeStore struct {
	mu      sync.Mutex
	records map[string]Record
	Err     error
}

func NewFakeStore() *FakeStore { return &FakeStore{records: map[string]Record{}} }

func (s *FakeStore) List(ctx context.Context) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	return out, nil
}

func (s *FakeStore) Save(ctx context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.records[r.ID] = r
	return nil
}
