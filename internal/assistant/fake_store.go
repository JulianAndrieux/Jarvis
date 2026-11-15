package assistant

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// FakeStore : Store en mémoire, cloisonné par environnement et par user
// comme MongoStore.
type FakeStore struct {
	shared *fakeShared
	scope  tenancy.Scope
}

type fakeShared struct {
	mu    sync.Mutex
	convs map[string]Conversation
}

// NewFakeStore rend une fake scopée sur l'identité locale.
func NewFakeStore() *FakeStore {
	return &FakeStore{shared: &fakeShared{convs: map[string]Conversation{}}, scope: tenancy.LocalScope()}
}

func (s *FakeStore) For(scope tenancy.Scope) Store { return &FakeStore{shared: s.shared, scope: scope} }

func (s *FakeStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("assistant: fake store: %w", err)
	}
	return nil
}

func (s *FakeStore) key(id string) string {
	return string(s.scope.Env) + "\x00" + string(s.scope.User) + "\x00" + id
}

func clone(c Conversation) Conversation {
	c.Messages = append([]Message(nil), c.Messages...)
	for i := range c.Messages {
		c.Messages[i].Steps = append([]Step(nil), c.Messages[i].Steps...)
	}
	c.Live = append([]Step(nil), c.Live...)
	return c
}

func (s *FakeStore) Create(ctx context.Context, c Conversation) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.convs[s.key(c.ID)]; ok {
		return fmt.Errorf("assistant: conversation %s déjà créée", c.ID)
	}
	c.Env, c.User = s.scope.Env, s.scope.User
	s.shared.convs[s.key(c.ID)] = clone(c)
	return nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Conversation, bool, error) {
	if err := s.ensure(); err != nil {
		return Conversation{}, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	c, ok := s.shared.convs[s.key(id)]
	return clone(c), ok, nil
}

func (s *FakeStore) List(ctx context.Context) ([]Conversation, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	var out []Conversation
	for _, c := range s.shared.convs {
		if c.Env == s.scope.Env && c.User == s.scope.User {
			// Comme MongoStore : une liste ne porte pas les messages.
			c.Messages, c.Live = nil, nil
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > MaxList {
		out = out[:MaxList]
	}
	return out, nil
}

func (s *FakeStore) Begin(ctx context.Context, id string, user Message, staleBefore time.Time) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	c, ok := s.shared.convs[s.key(id)]
	if !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	if c.Running && !c.RunStartedAt.Before(staleBefore) {
		return ErrBusy
	}
	c.Messages = append(append([]Message(nil), c.Messages...), user)
	c.Running, c.RunStartedAt, c.Live, c.UpdatedAt = true, user.At, nil, user.At
	s.shared.convs[s.key(id)] = c
	return nil
}

func (s *FakeStore) AddStep(ctx context.Context, id string, step Step) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	c, ok := s.shared.convs[s.key(id)]
	if !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	c.Live = append(append([]Step(nil), c.Live...), step)
	s.shared.convs[s.key(id)] = c
	return nil
}

func (s *FakeStore) Finish(ctx context.Context, id string, reply Message) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	c, ok := s.shared.convs[s.key(id)]
	if !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	c.Messages = append(append([]Message(nil), c.Messages...), reply)
	c.Running, c.RunStartedAt, c.Live, c.UpdatedAt = false, time.Time{}, nil, reply.At
	s.shared.convs[s.key(id)] = c
	return nil
}

func (s *FakeStore) Delete(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.convs[s.key(id)]; !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	delete(s.shared.convs, s.key(id))
	return nil
}
