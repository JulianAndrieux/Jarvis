package webapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

func (s *FakeStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	delete(s.jobs, id)
	return nil
}

func (s *FakeStore) List(ctx context.Context, q ListQuery) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := q.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	search := strings.ToLower(q.Search)

	matched := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if search == "" || jobMatchesSearch(j, search) {
			matched = append(matched, j)
		}
	}

	sort.Slice(matched, func(i, k int) bool { return matched[i].CreatedAt.After(matched[k].CreatedAt) })

	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func jobMatchesSearch(j Job, lowerSearch string) bool {
	if strings.Contains(strings.ToLower(j.Filename), lowerSearch) {
		return true
	}
	if strings.Contains(strings.ToLower(j.DocType), lowerSearch) {
		return true
	}
	for _, tag := range j.Tags {
		if strings.Contains(strings.ToLower(tag), lowerSearch) {
			return true
		}
	}
	if strings.Contains(strings.ToLower(j.SearchText), lowerSearch) {
		return true
	}
	return false
}
