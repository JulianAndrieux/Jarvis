package notes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Service : les opérations de l'interface sur les notes et les tâches.
type Service struct {
	Store Store
	// Now : horloge (nil : time.Now) — fixée dans les tests.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const untitled = "Sans titre"

// NewNote crée une note vide, liée au document docID s'il est donné.
func (s *Service) NewNote(ctx context.Context, docID string) (Note, error) {
	id, err := newID()
	if err != nil {
		return Note{}, err
	}
	now := s.now()
	n := Note{ID: id, Title: untitled, CreatedAt: now, UpdatedAt: now}
	if docID != "" {
		n.DocIDs = []string{docID}
	}
	if err := s.Store.CreateNote(ctx, n); err != nil {
		return Note{}, err
	}
	return n, nil
}

// SaveNote enregistre le contenu d'une note. tags : séparés par des
// virgules (doublons et vides retirés).
func (s *Service) SaveNote(ctx context.Context, id, title, body, tags string, pinned bool) (Note, error) {
	n, err := s.note(ctx, id)
	if err != nil {
		return Note{}, err
	}
	n.Title = strings.TrimSpace(title)
	if n.Title == "" {
		n.Title = untitled
	}
	n.Body, n.Tags, n.Pinned, n.UpdatedAt = body, splitTags(tags), pinned, s.now()
	return n, s.Store.UpdateNote(ctx, n)
}

// DeleteNote supprime une note ; ses tâches restent, déliées.
func (s *Service) DeleteNote(ctx context.Context, id string) error {
	tasks, err := s.Store.ListTasks(ctx, TaskQuery{NoteID: id})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		t.NoteID = ""
		if err := s.Store.UpdateTask(ctx, t); err != nil {
			return err
		}
	}
	return s.Store.DeleteNote(ctx, id)
}

// LinkDoc lie le document docID à la note (une seule fois).
func (s *Service) LinkDoc(ctx context.Context, noteID, docID string) error {
	n, err := s.note(ctx, noteID)
	if err != nil || docID == "" || slices.Contains(n.DocIDs, docID) {
		return err
	}
	n.DocIDs = append(n.DocIDs, docID)
	return s.Store.UpdateNote(ctx, n)
}

// UnlinkDoc retire le lien entre la note et le document docID.
func (s *Service) UnlinkDoc(ctx context.Context, noteID, docID string) error {
	n, err := s.note(ctx, noteID)
	if err != nil {
		return err
	}
	n.DocIDs = slices.DeleteFunc(n.DocIDs, func(d string) bool { return d == docID })
	return s.Store.UpdateNote(ctx, n)
}

// AddTask crée une tâche depuis une saisie rapide (cf. ParseQuickAdd),
// liée à la note et au document donnés ("" : aucun).
func (s *Service) AddTask(ctx context.Context, input, noteID, docID string) (Task, error) {
	now := s.now()
	q := ParseQuickAdd(input, now)
	if q.Title == "" {
		return Task{}, fmt.Errorf("la tâche est vide")
	}
	id, err := newID()
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, Title: q.Title, Due: q.Due, Priority: q.Priority, NoteID: noteID, DocID: docID, CreatedAt: now}
	return t, s.Store.CreateTask(ctx, t)
}

// AddMailTask crée une tâche depuis un email (saisie rapide, cf.
// ParseQuickAdd), liée à cet email.
func (s *Service) AddMailTask(ctx context.Context, input, mailID string) (Task, error) {
	now := s.now()
	q := ParseQuickAdd(input, now)
	if q.Title == "" {
		return Task{}, fmt.Errorf("la tâche est vide")
	}
	id, err := newID()
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, Title: q.Title, Due: q.Due, Priority: q.Priority, MailID: mailID, CreatedAt: now}
	return t, s.Store.CreateTask(ctx, t)
}

// NewMailNote crée une note depuis un email, liée à cet email.
func (s *Service) NewMailNote(ctx context.Context, mailID, title, body string) (Note, error) {
	id, err := newID()
	if err != nil {
		return Note{}, err
	}
	now := s.now()
	if title = strings.TrimSpace(title); title == "" {
		title = untitled
	}
	n := Note{ID: id, Title: title, Body: body, MailID: mailID, CreatedAt: now, UpdatedAt: now}
	return n, s.Store.CreateNote(ctx, n)
}

// ToggleTask coche ou décoche une tâche.
func (s *Service) ToggleTask(ctx context.Context, id string) (Task, error) {
	t, err := s.task(ctx, id)
	if err != nil {
		return Task{}, err
	}
	t.Done = !t.Done
	t.DoneAt = time.Time{}
	if t.Done {
		t.DoneAt = s.now()
	}
	return t, s.Store.UpdateTask(ctx, t)
}

// SaveTask enregistre une tâche modifiée. due : "AAAA-MM-JJ" ou "".
func (s *Service) SaveTask(ctx context.Context, id, title, due, priority, noteID, docID string) (Task, error) {
	t, err := s.task(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if t.Title = strings.TrimSpace(title); t.Title == "" {
		return Task{}, fmt.Errorf("le titre est vide")
	}
	if due != "" {
		d, err := time.Parse("2006-01-02", due)
		if err != nil || isoDate(d) != due {
			return Task{}, fmt.Errorf("échéance invalide %q (AAAA-MM-JJ attendu)", due)
		}
	}
	t.Due, t.Priority, t.NoteID, t.DocID = due, ParsePriority(priority), noteID, docID
	return t, s.Store.UpdateTask(ctx, t)
}

// DeleteTask supprime une tâche.
func (s *Service) DeleteTask(ctx context.Context, id string) error {
	return s.Store.DeleteTask(ctx, id)
}

func (s *Service) note(ctx context.Context, id string) (Note, error) {
	n, ok, err := s.Store.GetNote(ctx, id)
	if err != nil {
		return Note{}, err
	}
	if !ok {
		return Note{}, fmt.Errorf("note %s introuvable", id)
	}
	return n, nil
}

func (s *Service) task(ctx context.Context, id string) (Task, error) {
	t, ok, err := s.Store.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, fmt.Errorf("tâche %s introuvable", id)
	}
	return t, nil
}

func splitTags(raw string) []string {
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("notes: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
