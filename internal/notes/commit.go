package notes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Applier applique les opérations en attente portant sur des notes et des
// tâches. C'est ce paquet qui connaît leurs champs ; le commit lui-même
// reste générique (changes.Committer).
//
// Le contrôle se fait **par champ** : une opération s'applique si la valeur
// d'avant qu'elle a enregistrée est encore celle de la base. Deux personnes
// qui touchent des champs différents de la même note commitent donc toutes
// les deux sans conflit — c'est là que « commiter sans conflit » se gagne,
// par la granularité, pas par un algorithme de fusion. La version de
// l'entité, elle, ne sert qu'au diagnostic : elle bouge dès que n'importe
// quel champ change.
type Applier struct {
	Base Store
}

func (a Applier) Kinds() []changes.Kind {
	return []changes.Kind{changes.KindNote, changes.KindTask}
}

func (a Applier) Apply(ctx context.Context, scope tenancy.Scope, ops []changes.Op) ([]changes.Op, []changes.Conflict, error) {
	base := a.Base.For(scope)
	switch ops[0].Kind {
	case changes.KindNote:
		return commitNote(ctx, base, ops)
	case changes.KindTask:
		return commitTask(ctx, base, ops)
	}
	return nil, []changes.Conflict{{Op: ops[0], Reason: fmt.Sprintf("type d'entité inconnu (%s)", ops[0].Kind)}}, nil
}

func commitNote(ctx context.Context, base Store, ops []changes.Op) ([]changes.Op, []changes.Conflict, error) {
	id := ops[0].Target
	current, exists, err := base.GetNote(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	work := current
	var applied []changes.Op
	var conflicts []changes.Conflict
	create, remove := false, false

	for _, op := range ops {
		switch op.Action {
		case changes.Create:
			if exists {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "une note porte déjà cet identifiant"})
				continue
			}
			var created Note
			if err := json.Unmarshal([]byte(op.After), &created); err != nil {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "opération illisible"})
				continue
			}
			work, create = created, true
			applied = append(applied, op)
		case changes.Delete:
			if !exists {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "déjà supprimée"})
				continue
			}
			remove = true
			applied = append(applied, op)
		case changes.Set:
			if !exists && !create {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "supprimée entre-temps"})
				continue
			}
			f, ok := noteFieldByName(op.Field)
			if !ok {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: fmt.Sprintf("champ inconnu (%s)", op.Field)})
				continue
			}
			// Le contrôle par champ : si la valeur d'avant est encore là,
			// personne n'a touché ce champ, la modification s'applique.
			if now := changes.JSON(f.get(current)); !create && now != op.Before {
				conflicts = append(conflicts, changes.Conflict{
					Op:      op,
					Reason:  "ce champ a été modifié entre-temps",
					Current: now,
				})
				continue
			}
			if err := f.set(&work, json.RawMessage(op.After)); err != nil {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "valeur illisible"})
				continue
			}
			applied = append(applied, op)
		}
	}

	if len(applied) == 0 {
		return nil, conflicts, nil
	}
	switch {
	case remove:
		if err := base.DeleteNote(ctx, id); err != nil {
			return nil, nil, err
		}
	case create:
		if err := base.CreateNote(ctx, work); err != nil {
			return nil, nil, err
		}
	default:
		work.Version = current.Version
		if err := base.UpdateNote(ctx, work); err != nil {
			return nil, nil, err
		}
	}
	return applied, conflicts, nil
}

func commitTask(ctx context.Context, base Store, ops []changes.Op) ([]changes.Op, []changes.Conflict, error) {
	id := ops[0].Target
	current, exists, err := base.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	work := current
	var applied []changes.Op
	var conflicts []changes.Conflict
	create, remove := false, false

	for _, op := range ops {
		switch op.Action {
		case changes.Create:
			if exists {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "une tâche porte déjà cet identifiant"})
				continue
			}
			var created Task
			if err := json.Unmarshal([]byte(op.After), &created); err != nil {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "opération illisible"})
				continue
			}
			work, create = created, true
			applied = append(applied, op)
		case changes.Delete:
			if !exists {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "déjà supprimée"})
				continue
			}
			remove = true
			applied = append(applied, op)
		case changes.Set:
			if !exists && !create {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "supprimée entre-temps"})
				continue
			}
			f, ok := taskFieldByName(op.Field)
			if !ok {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: fmt.Sprintf("champ inconnu (%s)", op.Field)})
				continue
			}
			if now := changes.JSON(f.get(current)); !create && now != op.Before {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "ce champ a été modifié entre-temps", Current: now})
				continue
			}
			if err := f.set(&work, json.RawMessage(op.After)); err != nil {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "valeur illisible"})
				continue
			}
			applied = append(applied, op)
		}
	}

	if len(applied) == 0 {
		return nil, conflicts, nil
	}
	switch {
	case remove:
		if err := base.DeleteTask(ctx, id); err != nil {
			return nil, nil, err
		}
	case create:
		if err := base.CreateTask(ctx, work); err != nil {
			return nil, nil, err
		}
	default:
		work.Version = current.Version
		if err := base.UpdateTask(ctx, work); err != nil {
			return nil, nil, err
		}
	}
	return applied, conflicts, nil
}

func noteFieldByName(name string) (noteField, bool) {
	for _, f := range noteFields {
		if f.name == name {
			return f, true
		}
	}
	return noteField{}, false
}

func taskFieldByName(name string) (taskField, bool) {
	for _, f := range taskFields {
		if f.name == name {
			return f, true
		}
	}
	return taskField{}, false
}
