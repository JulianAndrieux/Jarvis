package webapp

import (
	"context"
	"fmt"
	"sync"
)

// FakeStore est une implémentation de test de Store : en mémoire, aucune
// base requise. Les jobs sont perdus à la fin du process — attendu pour
// un test.
type FakeStore struct {
	mu   sync.Mutex
	jobs map[string]Job
}

func NewFakeStore() *FakeStore {
	return &FakeStore{jobs: map[string]Job{}}
}

func (s *FakeStore) Create(ctx context.Context, job Job) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return job, nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok, nil
}

func (s *FakeStore) Update(ctx context.Context, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[job.ID]; !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", job.ID)
	}
	s.jobs[job.ID] = job
	return nil
}
