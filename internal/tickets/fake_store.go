package tickets

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// FakeStore est un Store en mémoire, pour les tests.
type FakeStore struct {
	mu      sync.Mutex
	tickets map[string]Ticket
}

func NewFakeStore() *FakeStore { return &FakeStore{tickets: map[string]Ticket{}} }

func (s *FakeStore) Create(ctx context.Context, t Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.ID] = t
	return nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Ticket, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[id]
	t.Events = append([]Event(nil), t.Events...)
	return t, ok, nil
}

func (s *FakeStore) Update(ctx context.Context, t Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.tickets[t.ID]
	if !ok {
		return fmt.Errorf("tickets: fake store: %s not found", t.ID)
	}
	t.Events = existing.Events // comme MongoStore : le fil n'est jamais réécrit
	s.tickets[t.ID] = t
	return nil
}

func (s *FakeStore) List(ctx context.Context, status Status) ([]Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Ticket
	for _, t := range s.tickets {
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *FakeStore) AppendEvent(ctx context.Context, id string, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[id]
	if !ok {
		return fmt.Errorf("tickets: fake store: %s not found", id)
	}
	t.Events = append(t.Events, e)
	s.tickets[id] = t
	return nil
}

func (s *FakeStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tickets[id]; !ok {
		return fmt.Errorf("tickets: fake store: %s not found", id)
	}
	delete(s.tickets, id)
	return nil
}
