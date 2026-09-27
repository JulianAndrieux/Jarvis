package notes

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// FakeStore est un Store en mémoire pour les tests, cloisonné par
// environnement comme MongoStore : les vues rendues par For partagent les
// mêmes données mais ne voient chacune que son environnement.
type FakeStore struct {
	shared *fakeShared
	scope  tenancy.Scope
}

// fakeShared est la « base » partagée par les vues scopées.
type fakeShared struct {
	mu    sync.Mutex
	notes map[string]Note // clé : env + "\x00" + id
	tasks map[string]Task // idem
}

// NewFakeStore rend une fake déjà scopée sur tenancy.Local.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		shared: &fakeShared{notes: map[string]Note{}, tasks: map[string]Task{}},
		scope:  tenancy.Scope{Env: tenancy.Local, Role: tenancy.RoleOwner},
	}
}

func (s *FakeStore) For(scope tenancy.Scope) Store {
	return &FakeStore{shared: s.shared, scope: scope}
}

func (s *FakeStore) key(id string) string { return string(s.scope.Env) + "\x00" + id }

// mine : la clé appartient-elle à l'environnement de cette vue.
func (s *FakeStore) mine(k string) bool {
	p := string(s.scope.Env) + "\x00"
	return len(k) >= len(p) && k[:len(p)] == p
}

// ensure refuse une opération sans environnement.
func (s *FakeStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("notes: fake store: %w", err)
	}
	return nil
}

func (s *FakeStore) CreateNote(ctx context.Context, n Note) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.notes[s.key(n.ID)]; ok {
		return fmt.Errorf("notes: note %s existe déjà", n.ID)
	}
	n.Env = s.scope.Env
	n.Version = 1
	s.shared.notes[s.key(n.ID)] = cloneNote(n)
	return nil
}

func (s *FakeStore) GetNote(ctx context.Context, id string) (Note, bool, error) {
	if err := s.ensure(); err != nil {
		return Note{}, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	n, ok := s.shared.notes[s.key(id)]
	return cloneNote(n), ok, nil
}

func (s *FakeStore) UpdateNote(ctx context.Context, n Note) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	existing, ok := s.shared.notes[s.key(n.ID)]
	if !ok {
		return fmt.Errorf("notes: note %s introuvable", n.ID)
	}
	if n.Version != existing.Version {
		return fmt.Errorf("%w: note %s (version %d, attendue %d)", ErrConflict, n.ID, n.Version, existing.Version)
	}
	n.Env = s.scope.Env
	n.Version = existing.Version + 1
	s.shared.notes[s.key(n.ID)] = cloneNote(n)
	return nil
}

func (s *FakeStore) DeleteNote(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.notes[s.key(id)]; !ok {
		return fmt.Errorf("notes: note %s introuvable", id)
	}
	delete(s.shared.notes, s.key(id))
	return nil
}

func (s *FakeStore) ListNotes(ctx context.Context, q NoteQuery) ([]Note, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	search := strings.ToLower(q.Search)
	var out []Note
	for k, n := range s.shared.notes {
		if !s.mine(k) {
			continue
		}
		if q.Tag != "" && !slices.Contains(n.Tags, q.Tag) {
			continue
		}
		if q.DocID != "" && !slices.Contains(n.DocIDs, q.DocID) {
			continue
		}
		if q.MailID != "" && n.MailID != q.MailID {
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
	return false
}

func (s *FakeStore) CreateTask(ctx context.Context, t Task) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.tasks[s.key(t.ID)]; ok {
		return fmt.Errorf("notes: tâche %s existe déjà", t.ID)
	}
	t.Env = s.scope.Env
	t.Version = 1
	s.shared.tasks[s.key(t.ID)] = t
	return nil
}

func (s *FakeStore) GetTask(ctx context.Context, id string) (Task, bool, error) {
	if err := s.ensure(); err != nil {
		return Task{}, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	t, ok := s.shared.tasks[s.key(id)]
	return t, ok, nil
}

func (s *FakeStore) UpdateTask(ctx context.Context, t Task) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	existing, ok := s.shared.tasks[s.key(t.ID)]
	if !ok {
		return fmt.Errorf("notes: tâche %s introuvable", t.ID)
	}
	if t.Version != existing.Version {
		return fmt.Errorf("%w: tâche %s (version %d, attendue %d)", ErrConflict, t.ID, t.Version, existing.Version)
	}
	t.Env = s.scope.Env
	t.Version = existing.Version + 1
	s.shared.tasks[s.key(t.ID)] = t
	return nil
}

func (s *FakeStore) DeleteTask(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.tasks[s.key(id)]; !ok {
		return fmt.Errorf("notes: tâche %s introuvable", id)
	}
	delete(s.shared.tasks, s.key(id))
	return nil
}

func (s *FakeStore) ListTasks(ctx context.Context, q TaskQuery) ([]Task, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	var out []Task
	for k, t := range s.shared.tasks {
		if !s.mine(k) {
			continue
		}
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
	return n
}
