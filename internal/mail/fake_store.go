package mail

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
// environnement comme MongoStore.
type FakeStore struct {
	shared *fakeShared
	scope  tenancy.Scope
}

type fakeShared struct {
	mu    sync.Mutex
	mails map[string]Mail   // clé : env + "\x00" + id
	files map[string][]byte // clé : env + "\x00" + id + "/" + index
}

// NewFakeStore rend une fake déjà scopée sur tenancy.Local.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		shared: &fakeShared{mails: map[string]Mail{}, files: map[string][]byte{}},
		scope:  tenancy.Scope{Env: tenancy.Local, Role: tenancy.RoleOwner},
	}
}

func (s *FakeStore) For(scope tenancy.Scope) Store {
	return &FakeStore{shared: s.shared, scope: scope}
}

func (s *FakeStore) key(id string) string { return string(s.scope.Env) + "\x00" + id }

func (s *FakeStore) mine(k string) bool {
	p := string(s.scope.Env) + "\x00"
	return len(k) >= len(p) && k[:len(p)] == p
}

func (s *FakeStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("mail: fake store: %w", err)
	}
	return nil
}

func fileKey(id string, index int) string { return fmt.Sprintf("%s/%d", id, index) }

func (s *FakeStore) Save(ctx context.Context, m Mail, files map[int][]byte) (bool, error) {
	if err := s.ensure(); err != nil {
		return false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.mails[s.key(m.ID)]; ok {
		return false, nil
	}
	m.Env = s.scope.Env
	s.shared.mails[s.key(m.ID)] = clone(m)
	for i, data := range files {
		s.shared.files[s.key(fileKey(m.ID, i))] = slices.Clone(data)
	}
	return true, nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Mail, bool, error) {
	if err := s.ensure(); err != nil {
		return Mail{}, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	m, ok := s.shared.mails[s.key(id)]
	return clone(m), ok, nil
}

func (s *FakeStore) List(ctx context.Context, q Query) ([]Mail, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	out := s.matching(q)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.After(out[j].Date)
		}
		return out[i].ID < out[j].ID
	})
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *FakeStore) Count(ctx context.Context, q Query) (int, error) {
	if err := s.ensure(); err != nil {
		return 0, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	return len(s.matching(q)), nil
}

// matching : les emails correspondant à q, dans le désordre (verrou pris).
func (s *FakeStore) matching(q Query) []Mail {
	search := strings.ToLower(q.Search)
	var out []Mail
	for k, m := range s.shared.mails {
		if !s.mine(k) {
			continue
		}
		if q.Category != "" && m.Triage.Category != q.Category {
			continue
		}
		if q.Reply && !m.Triage.Reply {
			continue
		}
		pending := (m.Triage.Category == "" && m.Triage.Error == "") || m.Triage.Version < TriageVersion
		if q.Untriaged && !pending {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(strings.Join([]string{m.Subject, m.From.Name, m.From.Email, m.Text, m.Triage.Summary}, "\x00")), search) {
			continue
		}
		out = append(out, clone(m))
	}
	return out
}

func (s *FakeStore) SetTriage(ctx context.Context, id string, t Triage) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	m, ok := s.shared.mails[s.key(id)]
	if !ok {
		return fmt.Errorf("mail: email %s introuvable", id)
	}
	m.Triage = t
	s.shared.mails[s.key(id)] = m
	return nil
}

func (s *FakeStore) SetAttachmentDoc(ctx context.Context, id string, index int, docID string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	m, ok := s.shared.mails[s.key(id)]
	if !ok {
		return fmt.Errorf("mail: email %s introuvable", id)
	}
	for i := range m.Attachments {
		if m.Attachments[i].Index == index {
			m.Attachments[i].DocID = docID
			s.shared.mails[s.key(id)] = m
			return nil
		}
	}
	return fmt.Errorf("mail: pièce jointe %d de %s introuvable", index, id)
}

func (s *FakeStore) Attachment(ctx context.Context, id string, index int) ([]byte, bool, error) {
	if err := s.ensure(); err != nil {
		return nil, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	data, ok := s.shared.files[s.key(fileKey(id, index))]
	return slices.Clone(data), ok, nil
}

func (s *FakeStore) LastUID(ctx context.Context, account, mailbox string, uidValidity uint32) (uint32, error) {
	if err := s.ensure(); err != nil {
		return 0, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	var last uint32
	for k, m := range s.shared.mails {
		if !s.mine(k) {
			continue
		}
		if m.Account == account && m.Mailbox == mailbox && m.UIDValidity == uidValidity && m.UID > last {
			last = m.UID
		}
	}
	return last, nil
}

func clone(m Mail) Mail {
	m.To = slices.Clone(m.To)
	m.Cc = slices.Clone(m.Cc)
	m.Attachments = slices.Clone(m.Attachments)
	return m
}
