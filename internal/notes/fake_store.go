package notes

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// FakeStore est un Store en mémoire pour les tests.
type FakeStore struct {
	mu    sync.Mutex
	notes map[string]Note
	tasks map[string]Task
}

func NewFakeStore() *FakeStore {
	return &FakeStore{notes: map[string]Note{}, tasks: map[string]Task{}}
}

func (s *FakeStore) CreateNote(ctx context.Context, n Note) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[n.ID]; ok {
		return fmt.Errorf("notes: note %s existe déjà", n.ID)
	}
	s.notes[n.ID] = cloneNote(n)
	return nil
}

func (s *FakeStore) GetNote(ctx context.Context, id string) (Note, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	return cloneNote(n), ok, nil
}

func (s *FakeStore) UpdateNote(ctx context.Context, n Note) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[n.ID]; !ok {
		return fmt.Errorf("notes: note %s introuvable", n.ID)
	}
	s.notes[n.ID] = cloneNote(n)
	return nil
}

func (s *FakeStore) DeleteNote(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[id]; !ok {
		return fmt.Errorf("notes: note %s introuvable", id)
	}
	delete(s.notes, id)
	return nil
}

func (s *FakeStore) ListNotes(ctx context.Context, q NoteQuery) ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	search := strings.ToLower(q.Search)
	var out []Note
	for _, n := range s.notes {
		if q.Tag != "" && !slices.Contains(n.Tags, q.Tag) && !slices.Contains(BlockTags(n.Blocks), q.Tag) {
			continue
		}
		if q.DocID != "" && !slices.Contains(n.DocIDs, q.DocID) {
			continue
		}
		if q.MailID != "" && n.MailID != q.MailID {
			continue
		}
		if !q.UpdatedFrom.IsZero() && n.UpdatedAt.Before(q.UpdatedFrom) {
			continue
		}
		if !q.UpdatedBefore.IsZero() && !n.UpdatedAt.Before(q.UpdatedBefore) {
			continue
		}
		if search != "" && !noteMatches(n, search) {
			continue
		}
		out = append(out, cloneNote(n))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func noteMatches(n Note, lowerSearch string) bool {
	if strings.Contains(strings.ToLower(n.Title), lowerSearch) || strings.Contains(strings.ToLower(n.Body), lowerSearch) {
		return true
	}
	for _, tag := range n.Tags {
		if strings.Contains(strings.ToLower(tag), lowerSearch) {
			return true
		}
	}
	for _, b := range n.Blocks {
		if strings.Contains(strings.ToLower(b.Text), lowerSearch) {
			return true
		}
		for _, tag := range b.Tags {
			if strings.Contains(strings.ToLower(tag), lowerSearch) {
				return true
			}
		}
	}
	return false
}

func (s *FakeStore) CreateTask(ctx context.Context, t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; ok {
		return fmt.Errorf("notes: tâche %s existe déjà", t.ID)
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *FakeStore) GetTask(ctx context.Context, id string) (Task, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	return t, ok, nil
}

func (s *FakeStore) UpdateTask(ctx context.Context, t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; !ok {
		return fmt.Errorf("notes: tâche %s introuvable", t.ID)
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *FakeStore) DeleteTask(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return fmt.Errorf("notes: tâche %s introuvable", id)
	}
	delete(s.tasks, id)
	return nil
}

func (s *FakeStore) ListTasks(ctx context.Context, q TaskQuery) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Task
	for _, t := range s.tasks {
		if (q.NoteID == "" || t.NoteID == q.NoteID) && (q.DocID == "" || t.DocID == q.DocID) && (q.MailID == "" || t.MailID == q.MailID) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// cloneNote : copie indépendante (les tranches ne sont pas partagées
// avec l'appelant, comme une note relue depuis MongoDB).
func cloneNote(n Note) Note {
	n.Tags = slices.Clone(n.Tags)
	n.DocIDs = slices.Clone(n.DocIDs)
	n.Blocks = slices.Clone(n.Blocks)
	for i := range n.Blocks {
		n.Blocks[i].Tags = slices.Clone(n.Blocks[i].Tags)
	}
	return n
}
