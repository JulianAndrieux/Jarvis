package assistant

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// FakeStore : Store en mémoire, cloisonné comme MongoStore.
type FakeStore struct {
	shared *fakeShared
	scope  tenancy.Scope
}

type fakeShared struct {
	mu    sync.Mutex
	convs map[string]Conversation // clé : owner + id
	facts map[string]Fact
	usage map[string]claudeapi.Usage // clé : env + jour
}

// NewFakeStore rend une fake scopée sur l'installation locale.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		shared: &fakeShared{convs: map[string]Conversation{}, facts: map[string]Fact{}, usage: map[string]claudeapi.Usage{}},
		scope:  tenancy.LocalScope(),
	}
}

func (s *FakeStore) For(scope tenancy.Scope) Store { return &FakeStore{shared: s.shared, scope: scope} }

func (s *FakeStore) owner() string {
	return string(s.scope.Env) + "\x00" + string(s.scope.User) + "\x00"
}
func (s *FakeStore) key(id string) string {
	return s.owner() + id
}

func (s *FakeStore) lock() (func(), error) {
	if err := s.scope.Valid(); err != nil {
		return nil, fmt.Errorf("assistant: fake store: %w", err)
	}
	s.shared.mu.Lock()
	return s.shared.mu.Unlock, nil
}

func (s *FakeStore) Create(ctx context.Context, c Conversation) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := s.shared.convs[s.key(c.ID)]; ok {
		return fmt.Errorf("assistant: conversation %s déjà créée", c.ID)
	}
	c.Env, c.User = s.scope.Env, s.scope.User
	c.Entries = append([]Entry(nil), c.Entries...)
	s.shared.convs[s.key(c.ID)] = c
	return nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Conversation, bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return Conversation{}, false, err
	}
	defer unlock()
	c, ok := s.shared.convs[s.key(id)]
	c.Entries = append([]Entry(nil), c.Entries...)
	return c, ok, nil
}

func (s *FakeStore) List(ctx context.Context, limit int) ([]Conversation, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if limit <= 0 {
		limit = DefaultListLimit
	}
	var out []Conversation
	for _, c := range s.shared.convs {
		if c.Env == s.scope.Env && c.User == s.scope.User {
			c.Entries = nil
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// update applique fn à la conversation id de la portée.
func (s *FakeStore) update(id string, fn func(c *Conversation)) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	c, ok := s.shared.convs[s.key(id)]
	if !ok {
		return fmt.Errorf("%w : conversation %s", ErrNotFound, id)
	}
	fn(&c)
	s.shared.convs[s.key(id)] = c
	return nil
}

func (s *FakeStore) Append(ctx context.Context, id string, at time.Time, entries ...Entry) error {
	return s.update(id, func(c *Conversation) {
		c.Entries = append(append([]Entry(nil), c.Entries...), entries...)
		c.UpdatedAt = at
	})
}

// transition : from → to, si la conversation est dans un des états from.
func (s *FakeStore) transition(id string, at time.Time, to Status, from ...Status) (bool, error) {
	done := false
	err := s.update(id, func(c *Conversation) {
		for _, f := range from {
			if c.Status == f {
				done = true
				c.Status, c.Error, c.UpdatedAt = to, "", at
			}
		}
	})
	return done, err
}

func (s *FakeStore) Start(ctx context.Context, id string, at time.Time) (bool, error) {
	return s.transition(id, at, StatusRunning, StatusIdle, StatusFailed)
}

func (s *FakeStore) Resume(ctx context.Context, id string, at time.Time) (bool, error) {
	return s.transition(id, at, StatusRunning, StatusWaiting)
}

func (s *FakeStore) Pause(ctx context.Context, id string, at time.Time) error {
	return s.update(id, func(c *Conversation) { c.Status, c.Error, c.UpdatedAt = StatusWaiting, "", at })
}

func (s *FakeStore) Finish(ctx context.Context, id string, errMsg string, at time.Time) error {
	return s.update(id, func(c *Conversation) {
		c.Status, c.Error, c.UpdatedAt = StatusIdle, errMsg, at
		if errMsg != "" {
			c.Status = StatusFailed
		}
	})
}

func (s *FakeStore) Delete(ctx context.Context, id string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := s.shared.convs[s.key(id)]; !ok {
		return fmt.Errorf("%w : conversation %s", ErrNotFound, id)
	}
	delete(s.shared.convs, s.key(id))
	return nil
}

// FailRunning : voir Recoverer.
func (s *FakeStore) FailRunning(ctx context.Context, errMsg string, at time.Time) (int, error) {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	n := 0
	for k, c := range s.shared.convs {
		if c.Status == StatusRunning {
			c.Status, c.Error, c.UpdatedAt = StatusFailed, errMsg, at
			s.shared.convs[k] = c
			n++
		}
	}
	return n, nil
}

func (s *FakeStore) AddFact(ctx context.Context, f Fact) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	s.shared.facts[s.key(f.ID)] = f
	return nil
}

func (s *FakeStore) Facts(ctx context.Context) ([]Fact, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []Fact
	for k, f := range s.shared.facts {
		if len(k) > len(s.owner()) && k[:len(s.owner())] == s.owner() {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *FakeStore) DeleteFact(ctx context.Context, id string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := s.shared.facts[s.key(id)]; !ok {
		return fmt.Errorf("%w : fait %s", ErrNotFound, id)
	}
	delete(s.shared.facts, s.key(id))
	return nil
}

func (s *FakeStore) AddUsage(ctx context.Context, day string, u claudeapi.Usage) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	k := string(s.scope.Env) + "\x00" + day
	s.shared.usage[k] = s.shared.usage[k].Add(u)
	return nil
}

func (s *FakeStore) UsageOn(ctx context.Context, day string) (claudeapi.Usage, error) {
	unlock, err := s.lock()
	if err != nil {
		return claudeapi.Usage{}, err
	}
	defer unlock()
	return s.shared.usage[string(s.scope.Env)+"\x00"+day], nil
}
