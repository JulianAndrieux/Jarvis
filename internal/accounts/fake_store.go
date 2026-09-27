package accounts

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// FakeStore est un Store en mémoire, pour les tests.
type FakeStore struct {
	mu       sync.Mutex
	users    map[tenancy.UserID]User
	envs     map[tenancy.EnvID]Environment
	members  map[string]Membership // clé : env + "\x00" + user
	sessions map[tenancy.SessionID]Session
}

func NewFakeStore() *FakeStore {
	return &FakeStore{
		users:    map[tenancy.UserID]User{},
		envs:     map[tenancy.EnvID]Environment{},
		members:  map[string]Membership{},
		sessions: map[tenancy.SessionID]Session{},
	}
}

func memberKey(e tenancy.EnvID, u tenancy.UserID) string { return string(e) + "\x00" + string(u) }

func (s *FakeStore) UpsertUser(ctx context.Context, u User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, existing := range s.users {
		if (u.GoogleSub != "" && existing.GoogleSub == u.GoogleSub) || strings.EqualFold(existing.Email, u.Email) {
			existing.Email, existing.Name = u.Email, u.Name
			if u.GoogleSub != "" {
				existing.GoogleSub = u.GoogleSub
			}
			s.users[id] = existing
			return existing, nil
		}
	}
	if u.ID == "" {
		return User{}, fmt.Errorf("accounts: fake store: un nouvel user doit avoir un identifiant")
	}
	s.users[u.ID] = u
	return u, nil
}

// SetUserDisabled : écriture ciblée, distincte d'UpsertUser (voir Store).
func (s *FakeStore) SetUserDisabled(ctx context.Context, id tenancy.UserID, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return fmt.Errorf("accounts: user %s introuvable", id)
	}
	u.Disabled = disabled
	s.users[id] = u
	return nil
}

func (s *FakeStore) UserByID(ctx context.Context, id tenancy.UserID) (User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	return u, ok, nil
}

func (s *FakeStore) UserByEmail(ctx context.Context, email string) (User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Email, email) {
			return u, true, nil
		}
	}
	return User{}, false, nil
}

func (s *FakeStore) ListUsers(ctx context.Context) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

func (s *FakeStore) CreateEnv(ctx context.Context, e Environment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.envs[e.ID]; ok {
		return fmt.Errorf("accounts: environnement %s existe déjà", e.ID)
	}
	s.envs[e.ID] = e
	return nil
}

func (s *FakeStore) Env(ctx context.Context, id tenancy.EnvID) (Environment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.envs[id]
	return e, ok, nil
}

func (s *FakeStore) ListEnvs(ctx context.Context) ([]Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Environment, 0, len(s.envs))
	for _, e := range s.envs {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *FakeStore) SetMembership(ctx context.Context, m Membership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberKey(m.Env, m.User)] = m
	return nil
}

func (s *FakeStore) MembershipsOfUser(ctx context.Context, u tenancy.UserID) ([]Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Membership
	for _, m := range s.members {
		if m.User == u {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Env < out[j].Env })
	return out, nil
}

func (s *FakeStore) MembershipsOfEnv(ctx context.Context, e tenancy.EnvID) ([]Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Membership
	for _, m := range s.members {
		if m.Env == e {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out, nil
}

func (s *FakeStore) RemoveMembership(ctx context.Context, e tenancy.EnvID, u tenancy.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memberKey(e, u)
	if _, ok := s.members[k]; !ok {
		return fmt.Errorf("accounts: appartenance %s/%s introuvable", e, u)
	}
	delete(s.members, k)
	return nil
}

func (s *FakeStore) CreateSession(ctx context.Context, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return nil
}

func (s *FakeStore) SessionByTokenHash(ctx context.Context, hash string) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.TokenHash == hash {
			return sess, true, nil
		}
	}
	return Session{}, false, nil
}

func (s *FakeStore) UpdateSession(ctx context.Context, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.sessions[sess.ID]
	if !ok {
		return fmt.Errorf("accounts: session %s introuvable", sess.ID)
	}
	// Comme MongoStore : ni le jeton ni le user ne sont réécrits.
	existing.Env, existing.LastSeenAt, existing.ExpiresAt = sess.Env, sess.LastSeenAt, sess.ExpiresAt
	s.sessions[sess.ID] = existing
	return nil
}

func (s *FakeStore) DeleteSession(ctx context.Context, id tenancy.SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("accounts: session %s introuvable", id)
	}
	delete(s.sessions, id)
	return nil
}

func (s *FakeStore) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, sess := range s.sessions {
		if sess.ExpiresAt.Before(before) {
			delete(s.sessions, id)
			n++
		}
	}
	return n, nil
}
