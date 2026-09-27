package notes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Overlay est un Store qui ne écrit rien : il met les écritures en attente
// dans le changeset de son user, et les applique à ses propres lectures.
//
// C'est le « vrai changeset » : mes modifications existent pour moi seul
// jusqu'à ce que je les commite. Les autres continuent de voir la base.
//
// Deux choses rendent ça tenable dans cette application :
//
//   - le filtrage des listes est déjà exprimable en Go (NoteQuery.Matches,
//     TaskQuery.Matches, extraits de la fake et surveillés par le contrat
//     commun Fake/Mongo). Sans ça, une note renommée en attente serait
//     trouvée par son ancien titre et introuvable par le nouveau, puisque
//     c'est Mongo qui filtre.
//   - quand rien n'est en attente — le cas courant — l'overlay délègue
//     directement à la base, sans rien recalculer.
type Overlay struct {
	// Base est la persistance réelle, déjà scopée.
	Base Store
	// Staged tient les opérations en attente.
	Staged changes.ChangesetStore
	// Stager les attribue et les enregistre.
	Stager *changes.Stager

	scope tenancy.Scope
}

// NewOverlay construit l'overlay d'une portée.
func NewOverlay(base Store, staged changes.ChangesetStore, stager *changes.Stager, scope tenancy.Scope) *Overlay {
	return &Overlay{Base: base.For(scope), Staged: staged, Stager: stager, scope: scope}
}

func (o *Overlay) For(scope tenancy.Scope) Store {
	return &Overlay{Base: o.Base.For(scope), Staged: o.Staged, Stager: o.Stager, scope: scope}
}

// pending : mes opérations en attente, dans l'ordre où je les ai faites.
func (o *Overlay) pending(ctx context.Context) ([]changes.Op, error) {
	return o.Staged.Pending(ctx, o.scope.Env, o.scope.User)
}

// --- Notes ---

func (o *Overlay) CreateNote(ctx context.Context, n Note) error {
	n.Env = o.scope.Env
	return o.Stager.Stage(ctx, changes.Op{
		Kind: changes.KindNote, Target: n.ID, Label: n.Title,
		Action: changes.Create, After: changes.JSON(n),
	})
}

func (o *Overlay) GetNote(ctx context.Context, id string) (Note, bool, error) {
	n, ok, err := o.Base.GetNote(ctx, id)
	if err != nil {
		return Note{}, false, err
	}
	ops, err := o.pending(ctx)
	if err != nil {
		return Note{}, false, err
	}
	n, ok = applyNoteOps(n, ok, id, ops)
	return n, ok, nil
}

func (o *Overlay) UpdateNote(ctx context.Context, n Note) error {
	base, ok, err := o.Base.GetNote(ctx, n.ID)
	if err != nil {
		return err
	}
	ops, err := o.pending(ctx)
	if err != nil {
		return err
	}
	// L'état que je vois (base + mes opérations) : c'est par rapport à lui
	// qu'il faut diffuser, sinon une seconde modification du même champ
	// serait prise pour un retour en arrière.
	seen, seenOK := applyNoteOps(base, ok, n.ID, ops)
	if !seenOK {
		return fmt.Errorf("notes: note %s introuvable", n.ID)
	}
	var staged []changes.Op
	for _, f := range noteFields {
		before, after := f.get(seen), f.get(n)
		if changes.JSON(before) == changes.JSON(after) {
			continue
		}
		staged = append(staged, changes.Op{
			Kind: changes.KindNote, Target: n.ID, Label: n.Title, Field: f.name,
			Action: changes.Set, Before: changes.JSON(f.get(base)), After: changes.JSON(after),
			BaseVer: base.Version,
		})
	}
	if len(staged) == 0 {
		return nil
	}
	return o.Stager.Stage(ctx, staged...)
}

func (o *Overlay) DeleteNote(ctx context.Context, id string) error {
	n, ok, err := o.GetNote(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("notes: note %s introuvable", id)
	}
	return o.Stager.Stage(ctx, changes.Op{
		Kind: changes.KindNote, Target: id, Label: n.Title, Action: changes.Delete, BaseVer: n.Version,
	})
}

func (o *Overlay) ListNotes(ctx context.Context, q NoteQuery) ([]Note, error) {
	ops, err := o.pending(ctx)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return o.Base.ListNotes(ctx, q) // rien en attente : le cas courant
	}
	// La requête est appliquée en Go, après mes opérations : une note que
	// j'ai renommée doit sortir sur son nouveau nom, et pas sur l'ancien.
	// D'où une lecture non filtrée de la base — acceptable ici, les
	// collections sont petites et bornées, et ce chemin ne sert que quand
	// j'ai des modifications en attente.
	all, err := o.Base.ListNotes(ctx, NoteQuery{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Note
	for _, n := range all {
		seen[n.ID] = true
		if merged, ok := applyNoteOps(n, true, n.ID, ops); ok && q.Matches(merged) {
			out = append(out, merged)
		}
	}
	// Les notes créées en attente n'existent pas dans la base.
	for _, op := range ops {
		if op.Kind != changes.KindNote || op.Action != changes.Create || seen[op.Target] {
			continue
		}
		if merged, ok := applyNoteOps(Note{}, false, op.Target, ops); ok && q.Matches(merged) {
			out = append(out, merged)
			seen[op.Target] = true
		}
	}
	SortNotes(out)
	return out, nil
}

// --- Tâches ---

func (o *Overlay) CreateTask(ctx context.Context, t Task) error {
	t.Env = o.scope.Env
	return o.Stager.Stage(ctx, changes.Op{
		Kind: changes.KindTask, Target: t.ID, Label: t.Title,
		Action: changes.Create, After: changes.JSON(t),
	})
}

func (o *Overlay) GetTask(ctx context.Context, id string) (Task, bool, error) {
	t, ok, err := o.Base.GetTask(ctx, id)
	if err != nil {
		return Task{}, false, err
	}
	ops, err := o.pending(ctx)
	if err != nil {
		return Task{}, false, err
	}
	t, ok = applyTaskOps(t, ok, id, ops)
	return t, ok, nil
}

func (o *Overlay) UpdateTask(ctx context.Context, t Task) error {
	base, ok, err := o.Base.GetTask(ctx, t.ID)
	if err != nil {
		return err
	}
	ops, err := o.pending(ctx)
	if err != nil {
		return err
	}
	seen, seenOK := applyTaskOps(base, ok, t.ID, ops)
	if !seenOK {
		return fmt.Errorf("notes: tâche %s introuvable", t.ID)
	}
	var staged []changes.Op
	for _, f := range taskFields {
		before, after := f.get(seen), f.get(t)
		if changes.JSON(before) == changes.JSON(after) {
			continue
		}
		staged = append(staged, changes.Op{
			Kind: changes.KindTask, Target: t.ID, Label: t.Title, Field: f.name,
			Action: changes.Set, Before: changes.JSON(f.get(base)), After: changes.JSON(after),
			BaseVer: base.Version,
		})
	}
	if len(staged) == 0 {
		return nil
	}
	return o.Stager.Stage(ctx, staged...)
}

func (o *Overlay) DeleteTask(ctx context.Context, id string) error {
	t, ok, err := o.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("notes: tâche %s introuvable", id)
	}
	return o.Stager.Stage(ctx, changes.Op{
		Kind: changes.KindTask, Target: id, Label: t.Title, Action: changes.Delete, BaseVer: t.Version,
	})
}

func (o *Overlay) ListTasks(ctx context.Context, q TaskQuery) ([]Task, error) {
	ops, err := o.pending(ctx)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return o.Base.ListTasks(ctx, q)
	}
	all, err := o.Base.ListTasks(ctx, TaskQuery{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Task
	for _, t := range all {
		seen[t.ID] = true
		if merged, ok := applyTaskOps(t, true, t.ID, ops); ok && q.Matches(merged) {
			out = append(out, merged)
		}
	}
	for _, op := range ops {
		if op.Kind != changes.KindTask || op.Action != changes.Create || seen[op.Target] {
			continue
		}
		if merged, ok := applyTaskOps(Task{}, false, op.Target, ops); ok && q.Matches(merged) {
			out = append(out, merged)
			seen[op.Target] = true
		}
	}
	SortTasks(out)
	return out, nil
}

// --- Application des opérations ---

// noteField décrit un champ modifiable d'une note : comment le lire, et
// comment y écrire une valeur venue du JSON d'une opération. Une table
// plutôt qu'un switch géant : ajouter un champ éditable, c'est une ligne,
// et les deux sens restent côte à côte.
type noteField struct {
	name string
	get  func(Note) any
	set  func(*Note, json.RawMessage) error
}

var noteFields = []noteField{
	{"title", func(n Note) any { return n.Title }, func(n *Note, v json.RawMessage) error { return json.Unmarshal(v, &n.Title) }},
	{"body", func(n Note) any { return n.Body }, func(n *Note, v json.RawMessage) error { return json.Unmarshal(v, &n.Body) }},
	{"tags", func(n Note) any { return n.Tags }, func(n *Note, v json.RawMessage) error { return json.Unmarshal(v, &n.Tags) }},
	{"pinned", func(n Note) any { return n.Pinned }, func(n *Note, v json.RawMessage) error { return json.Unmarshal(v, &n.Pinned) }},
	{"doc_ids", func(n Note) any { return n.DocIDs }, func(n *Note, v json.RawMessage) error { return json.Unmarshal(v, &n.DocIDs) }},
}

type taskField struct {
	name string
	get  func(Task) any
	set  func(*Task, json.RawMessage) error
}

var taskFields = []taskField{
	{"title", func(t Task) any { return t.Title }, func(t *Task, v json.RawMessage) error { return json.Unmarshal(v, &t.Title) }},
	{"due", func(t Task) any { return t.Due }, func(t *Task, v json.RawMessage) error { return json.Unmarshal(v, &t.Due) }},
	{"priority", func(t Task) any { return t.Priority }, func(t *Task, v json.RawMessage) error { return json.Unmarshal(v, &t.Priority) }},
	{"done", func(t Task) any { return t.Done }, func(t *Task, v json.RawMessage) error { return json.Unmarshal(v, &t.Done) }},
	{"note_id", func(t Task) any { return t.NoteID }, func(t *Task, v json.RawMessage) error { return json.Unmarshal(v, &t.NoteID) }},
	{"doc_id", func(t Task) any { return t.DocID }, func(t *Task, v json.RawMessage) error { return json.Unmarshal(v, &t.DocID) }},
}

// applyNoteOps superpose les opérations visant id à l'état de base.
// ok=false en sortie signifie « n'existe pas de mon point de vue » : soit
// elle n'existait pas, soit je l'ai supprimée en attente.
func applyNoteOps(base Note, exists bool, id string, ops []changes.Op) (Note, bool) {
	n := base
	for _, op := range ops {
		if op.Kind != changes.KindNote || op.Target != id {
			continue
		}
		switch op.Action {
		case changes.Create:
			var created Note
			if json.Unmarshal([]byte(op.After), &created) == nil {
				n, exists = created, true
			}
		case changes.Delete:
			exists = false
		case changes.Set:
			if !exists {
				continue
			}
			for _, f := range noteFields {
				if f.name == op.Field {
					_ = f.set(&n, json.RawMessage(op.After))
				}
			}
		}
	}
	return n, exists
}

func applyTaskOps(base Task, exists bool, id string, ops []changes.Op) (Task, bool) {
	t := base
	for _, op := range ops {
		if op.Kind != changes.KindTask || op.Target != id {
			continue
		}
		switch op.Action {
		case changes.Create:
			var created Task
			if json.Unmarshal([]byte(op.After), &created) == nil {
				t, exists = created, true
			}
		case changes.Delete:
			exists = false
		case changes.Set:
			if !exists {
				continue
			}
			for _, f := range taskFields {
				if f.name == op.Field {
					_ = f.set(&t, json.RawMessage(op.After))
				}
			}
		}
	}
	return t, exists
}
