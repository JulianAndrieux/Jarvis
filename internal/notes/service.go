package notes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Service : les opérations de l'interface sur les notes et les tâches.
type Service struct {
	Store Store
	// Now : horloge (nil : time.Now) — fixée dans les tests.
	Now func() time.Time
	// Changes, s'il est donné, journalise les modifications faites par un
	// humain (jalon 46) : « qui a changé quoi ». Une opération par champ
	// réellement modifié — c'est la granularité dont le changeset (jalon
	// 48) aura besoin, pas une trace « la note a changé ».
	Changes *changes.Recorder
	// Staged et Stager, s'ils sont donnés, mettent les écritures en attente
	// dans le changeset du user au lieu de les appliquer (jalon 48) : mes
	// modifications n'existent que pour moi jusqu'à ce que je les commite.
	// Le travail de fond, lui, écrit toujours directement — il n'a pas de
	// session, donc rien à mettre en attente.
	Staged changes.ChangesetStore
	Stager *changes.Stager
	// DefaultScope est la portée utilisée quand le contexte n'en porte
	// pas : choix de câblage explicite (l'unique environnement
	// d'aujourd'hui), que le jalon 45 remplacera par la portée de la
	// session.
	DefaultScope tenancy.Scope
}

// db rend la persistance vue depuis la portée de cette opération — le seul
// point du Service où l'environnement entre en jeu.
func (s *Service) db(ctx context.Context) Store {
	scope, ok := tenancy.FromContext(ctx)
	if !ok {
		scope = s.DefaultScope
		if scope.Env == "" {
			scope = tenancy.Scope{Env: tenancy.Local, Role: tenancy.RoleOwner}
		}
	}
	if s.Staged != nil && s.Stager != nil && !scope.Background() {
		return NewOverlay(s.Store, s.Staged, s.Stager, scope)
	}
	return s.Store.For(scope)
}

// Pending : mes opérations en attente (vide si le changeset n'est pas
// activé).
func (s *Service) Pending(ctx context.Context) ([]changes.Op, error) {
	scope, ok := tenancy.FromContext(ctx)
	if !ok || s.Staged == nil || scope.Background() {
		return nil, nil
	}
	return s.Staged.Pending(ctx, scope.Env, scope.User)
}

// Commit applique mes opérations en attente. Rend ce qui a été appliqué et
// ce qui reste en conflit.
func (s *Service) Commit(ctx context.Context) (changes.CommitResult, error) {
	scope, ok := tenancy.FromContext(ctx)
	if !ok || s.Staged == nil {
		return changes.CommitResult{}, fmt.Errorf("notes: aucun changeset")
	}
	c := &changes.Committer{Staged: s.Staged, Appliers: []changes.Applier{Applier{Base: s.Store}}}
	return c.Commit(ctx, scope)
}

// DiscardAll abandonne toutes mes opérations en attente.
func (s *Service) DiscardAll(ctx context.Context) error {
	scope, ok := tenancy.FromContext(ctx)
	if !ok || s.Staged == nil {
		return fmt.Errorf("notes: aucun changeset")
	}
	return s.Staged.DropAll(ctx, scope.Env, scope.User)
}

// Discard abandonne une opération précise.
func (s *Service) Discard(ctx context.Context, opID string) error {
	scope, ok := tenancy.FromContext(ctx)
	if !ok || s.Staged == nil {
		return fmt.Errorf("notes: aucun changeset")
	}
	return s.Staged.Drop(ctx, scope.Env, scope.User, opID)
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
	if err := s.db(ctx).CreateNote(ctx, n); err != nil {
		return Note{}, err
	}
	s.Changes.Record(ctx, noteOp(n, "", changes.Create))
	return n, nil
}

// SaveNote enregistre le contenu d'une note. tags : séparés par des
// virgules (doublons et vides retirés).
//
// Écrit sur la version qu'elle vient de lire : la fenêtre de conflit est
// donc minuscule. Un formulaire ouvert depuis dix minutes, lui, doit
// passer par SaveNoteVersion en portant la version qu'il a affichée —
// sinon il écraserait sans le savoir ce qui a changé depuis.
func (s *Service) SaveNote(ctx context.Context, id, title, body, tags string, pinned bool) (Note, error) {
	n, err := s.note(ctx, id)
	if err != nil {
		return Note{}, err
	}
	return s.saveNote(ctx, n, title, body, tags, pinned)
}

// SaveNoteVersion enregistre une note en exigeant que rien n'ait changé
// depuis la version affichée. En cas de conflit, rend un *Conflict portant
// l'état courant, pour que l'interface puisse montrer les deux côtés.
func (s *Service) SaveNoteVersion(ctx context.Context, id string, version int, title, body, tags string, pinned bool) (Note, error) {
	n, err := s.note(ctx, id)
	if err != nil {
		return Note{}, err
	}
	if n.Version != version {
		return Note{}, &Conflict{Current: n}
	}
	return s.saveNote(ctx, n, title, body, tags, pinned)
}

func (s *Service) saveNote(ctx context.Context, n Note, title, body, tags string, pinned bool) (Note, error) {
	// L'état d'avant est capturé ici, avant toute écriture : le journal
	// compare des champs, pas des intentions.
	before := n
	n.Title = strings.TrimSpace(title)
	if n.Title == "" {
		n.Title = untitled
	}
	n.Body, n.Tags, n.Pinned, n.UpdatedAt = body, splitTags(tags), pinned, s.now()
	if err := s.db(ctx).UpdateNote(ctx, n); err != nil {
		if errors.Is(err, ErrConflict) {
			// Quelqu'un a écrit dans la fenêtre entre la lecture et
			// l'écriture : rendre l'état courant plutôt qu'une erreur nue.
			if current, ok, gerr := s.db(ctx).GetNote(ctx, n.ID); gerr == nil && ok {
				return Note{}, &Conflict{Current: current}
			}
		}
		return Note{}, err
	}
	var ops []changes.Op
	for _, c := range []struct {
		field         string
		before, after any
	}{
		{"title", before.Title, n.Title},
		{"body", before.Body, n.Body},
		{"tags", before.Tags, n.Tags},
		{"pinned", before.Pinned, n.Pinned},
	} {
		if op, ok := setOp(noteOp(n, c.field, changes.Set), c.before, c.after); ok {
			ops = append(ops, op)
		}
	}
	s.Changes.Record(ctx, ops...)
	return n, nil
}

// DeleteNote supprime une note ; ses tâches restent, déliées.
func (s *Service) DeleteNote(ctx context.Context, id string) error {
	tasks, err := s.db(ctx).ListTasks(ctx, TaskQuery{NoteID: id})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		t.NoteID = ""
		if err := s.db(ctx).UpdateTask(ctx, t); err != nil {
			return err
		}
	}
	n, err := s.note(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db(ctx).DeleteNote(ctx, id); err != nil {
		return err
	}
	s.Changes.Record(ctx, noteOp(n, "", changes.Delete))
	return nil
}

// LinkDoc lie le document docID à la note (une seule fois).
func (s *Service) LinkDoc(ctx context.Context, noteID, docID string) error {
	n, err := s.note(ctx, noteID)
	if err != nil || docID == "" || slices.Contains(n.DocIDs, docID) {
		return err
	}
	before := n.DocIDs
	n.DocIDs = append(n.DocIDs, docID)
	if err := s.db(ctx).UpdateNote(ctx, n); err != nil {
		return err
	}
	if op, ok := setOp(noteOp(n, "doc_ids", changes.Set), before, n.DocIDs); ok {
		s.Changes.Record(ctx, op)
	}
	return nil
}

// UnlinkDoc retire le lien entre la note et le document docID.
func (s *Service) UnlinkDoc(ctx context.Context, noteID, docID string) error {
	n, err := s.note(ctx, noteID)
	if err != nil {
		return err
	}
	before := append([]string(nil), n.DocIDs...)
	n.DocIDs = slices.DeleteFunc(n.DocIDs, func(d string) bool { return d == docID })
	if err := s.db(ctx).UpdateNote(ctx, n); err != nil {
		return err
	}
	if op, ok := setOp(noteOp(n, "doc_ids", changes.Set), before, n.DocIDs); ok {
		s.Changes.Record(ctx, op)
	}
	return nil
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
	if err := s.db(ctx).CreateTask(ctx, t); err != nil {
		return Task{}, err
	}
	s.Changes.Record(ctx, taskOp(t, "", changes.Create))
	return t, nil
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
	if err := s.db(ctx).CreateTask(ctx, t); err != nil {
		return Task{}, err
	}
	s.Changes.Record(ctx, taskOp(t, "", changes.Create))
	return t, nil
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
	if err := s.db(ctx).CreateNote(ctx, n); err != nil {
		return Note{}, err
	}
	s.Changes.Record(ctx, noteOp(n, "", changes.Create))
	return n, nil
}

// ToggleTask coche ou décoche une tâche.
func (s *Service) ToggleTask(ctx context.Context, id string) (Task, error) {
	t, err := s.task(ctx, id)
	if err != nil {
		return Task{}, err
	}
	was := t.Done
	t.Done = !t.Done
	t.DoneAt = time.Time{}
	if t.Done {
		t.DoneAt = s.now()
	}
	if err := s.db(ctx).UpdateTask(ctx, t); err != nil {
		return Task{}, err
	}
	if op, ok := setOp(taskOp(t, "done", changes.Set), was, t.Done); ok {
		s.Changes.Record(ctx, op)
	}
	return t, nil
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
	before := t
	t.Due, t.Priority, t.NoteID, t.DocID = due, ParsePriority(priority), noteID, docID
	if err := s.db(ctx).UpdateTask(ctx, t); err != nil {
		return Task{}, err
	}
	var ops []changes.Op
	for _, c := range []struct {
		field         string
		before, after any
	}{
		{"title", before.Title, t.Title},
		{"due", before.Due, t.Due},
		{"priority", before.Priority, t.Priority},
		{"note_id", before.NoteID, t.NoteID},
		{"doc_id", before.DocID, t.DocID},
	} {
		if op, ok := setOp(taskOp(t, c.field, changes.Set), c.before, c.after); ok {
			ops = append(ops, op)
		}
	}
	s.Changes.Record(ctx, ops...)
	return t, nil
}

// DeleteTask supprime une tâche.
func (s *Service) DeleteTask(ctx context.Context, id string) error {
	t, err := s.task(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db(ctx).DeleteTask(ctx, id); err != nil {
		return err
	}
	s.Changes.Record(ctx, taskOp(t, "", changes.Delete))
	return nil
}

func (s *Service) note(ctx context.Context, id string) (Note, error) {
	n, ok, err := s.db(ctx).GetNote(ctx, id)
	if err != nil {
		return Note{}, err
	}
	if !ok {
		return Note{}, fmt.Errorf("note %s introuvable", id)
	}
	return n, nil
}

func (s *Service) task(ctx context.Context, id string) (Task, error) {
	t, ok, err := s.db(ctx).GetTask(ctx, id)
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

// noteOp et taskOp : une opération du journal visant cette entité.
func noteOp(n Note, field string, action changes.Action) changes.Op {
	return changes.Op{Kind: changes.KindNote, Target: n.ID, Label: n.Title, Field: field, Action: action}
}

func taskOp(t Task, field string, action changes.Action) changes.Op {
	return changes.Op{Kind: changes.KindTask, Target: t.ID, Label: t.Title, Field: field, Action: action}
}

// setOp : une opération « ce champ passe de before à after », ou rien du
// tout si la valeur n'a pas bougé. Un enregistrement sans modification ne
// doit pas remplir le journal.
func setOp(base changes.Op, before, after any) (changes.Op, bool) {
	b, a := changes.JSON(before), changes.JSON(after)
	if b == a {
		return changes.Op{}, false
	}
	base.Action, base.Before, base.After = changes.Set, b, a
	return base, true
}
