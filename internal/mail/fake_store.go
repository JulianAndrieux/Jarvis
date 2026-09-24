package mail

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
	mails map[string]Mail
	files map[string][]byte // clé : id + "/" + index
}

func NewFakeStore() *FakeStore {
	return &FakeStore{mails: map[string]Mail{}, files: map[string][]byte{}}
}

func fileKey(id string, index int) string { return fmt.Sprintf("%s/%d", id, index) }

func (s *FakeStore) Save(ctx context.Context, m Mail, files map[int][]byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mails[m.ID]; ok {
		return false, nil
	}
	s.mails[m.ID] = clone(m)
	for i, data := range files {
		s.files[fileKey(m.ID, i)] = slices.Clone(data)
	}
	return true, nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Mail, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mails[id]
	return clone(m), ok, nil
}

func (s *FakeStore) List(ctx context.Context, q Query) ([]Mail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	search := strings.ToLower(q.Search)
	var out []Mail
	for _, m := range s.mails {
		if q.Category != "" && m.Triage.Category != q.Category {
			continue
		}
		if q.Untriaged && (m.Triage.Category != "" || m.Triage.Error != "") {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(strings.Join([]string{m.Subject, m.From.Name, m.From.Email, m.Text, m.Triage.Summary}, "\x00")), search) {
			continue
		}
		out = append(out, clone(m))
	}
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

func (s *FakeStore) SetTriage(ctx context.Context, id string, t Triage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mails[id]
	if !ok {
		return fmt.Errorf("mail: email %s introuvable", id)
	}
	m.Triage = t
	s.mails[id] = m
	return nil
}

func (s *FakeStore) SetAttachmentDoc(ctx context.Context, id string, index int, docID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mails[id]
	if !ok {
		return fmt.Errorf("mail: email %s introuvable", id)
	}
	for i := range m.Attachments {
		if m.Attachments[i].Index == index {
			m.Attachments[i].DocID = docID
			s.mails[id] = m
			return nil
		}
	}
	return fmt.Errorf("mail: pièce jointe %d de %s introuvable", index, id)
}

func (s *FakeStore) Attachment(ctx context.Context, id string, index int) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[fileKey(id, index)]
	return slices.Clone(data), ok, nil
}

func (s *FakeStore) LastUID(ctx context.Context, account, mailbox string, uidValidity uint32) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last uint32
	for _, m := range s.mails {
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
