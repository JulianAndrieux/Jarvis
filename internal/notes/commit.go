package notes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// CommitConflict : une opération en attente qui ne peut pas s'appliquer,
// avec de quoi l'expliquer et choisir.
type CommitConflict struct {
	Op changes.Op
	// Reason, en français, dit ce qui s'est passé.
	Reason string
	// Current : la valeur du champ telle qu'elle est aujourd'hui (JSON),
	// pour montrer les deux côtés comme une revue de diff.
	Current string
}

// CommitResult : ce qu'un commit a fait.
type CommitResult struct {
	// Applied : nombre d'opérations appliquées.
	Applied int
	// Conflicts : celles qui restent en attente, avec leur raison. Rien
	// n'est appliqué en silence par-dessus une modification de quelqu'un
	// d'autre.
	Conflicts []CommitConflict
}

// Committer applique le changeset d'un user.
//
// Le contrôle se fait **par champ** : une opération s'applique si la valeur
// d'avant qu'elle a enregistrée est encore celle de la base. Deux personnes
// qui touchent des champs différents de la même note commitent donc toutes
// les deux sans conflit — c'est là que « commiter sans conflit » se gagne,
// par la granularité, pas par un algorithme de fusion. La version de
// l'entité, elle, ne sert qu'au diagnostic : elle bouge dès que n'importe
// quel champ change.
type Committer struct {
	Base   Store
	Staged changes.ChangesetStore
}

// Commit applique, pour la portée donnée, tout ce qui peut l'être. Les
// opérations appliquées quittent le changeset ; celles en conflit y
// restent, pour que l'utilisateur tranche.
func (c *Committer) Commit(ctx context.Context, scope tenancy.Scope) (CommitResult, error) {
	var res CommitResult
	if err := scope.Valid(); err != nil {
		return res, err
	}
	ops, err := c.Staged.Pending(ctx, scope.Env, scope.User)
	if err != nil {
		return res, err
	}
	base := c.Base.For(scope)

	// Par entité, dans l'ordre d'apparition : une modification peut en
	// suivre une autre sur la même note.
	order, groups := groupByTarget(ops)
	for _, key := range order {
		applied, conflicts, err := c.commitTarget(ctx, base, groups[key])
		if err != nil {
			return res, err
		}
		res.Conflicts = append(res.Conflicts, conflicts...)
		for _, op := range applied {
			if err := c.Staged.Drop(ctx, scope.Env, scope.User, op.ID); err != nil {
				return res, err
			}
			res.Applied++
		}
	}
	return res, nil
}

type targetKey struct {
	kind   changes.Kind
	target string
}

func groupByTarget(ops []changes.Op) ([]targetKey, map[targetKey][]changes.Op) {
	groups := map[targetKey][]changes.Op{}
	var order []targetKey
	for _, op := range ops {
		k := targetKey{op.Kind, op.Target}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], op)
	}
	return order, groups
}

func (c *Committer) commitTarget(ctx context.Context, base Store, ops []changes.Op) ([]changes.Op, []CommitConflict, error) {
	switch ops[0].Kind {
	case changes.KindNote:
		return commitNote(ctx, base, ops)
	case changes.KindTask:
		return commitTask(ctx, base, ops)
	}
	return nil, []CommitConflict{{Op: ops[0], Reason: fmt.Sprintf("type d'entité inconnu (%s)", ops[0].Kind)}}, nil
}

func commitNote(ctx context.Context, base Store, ops []changes.Op) ([]changes.Op, []CommitConflict, error) {
	id := ops[0].Target
	current, exists, err := base.GetNote(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	work := current
	var applied []changes.Op
	var conflicts []CommitConflict
	create, remove := false, false

	for _, op := range ops {
		switch op.Action {
		case changes.Create:
			if exists {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "une note porte déjà cet identifiant"})
				continue
			}
			var created Note
			if err := json.Unmarshal([]byte(op.After), &created); err != nil {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "opération illisible"})
				continue
			}
			work, create = created, true
			applied = append(applied, op)
		case changes.Delete:
			if !exists {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "déjà supprimée"})
				continue
			}
			remove = true
			applied = append(applied, op)
		case changes.Set:
			if !exists && !create {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "supprimée entre-temps"})
				continue
			}
			f, ok := noteFieldByName(op.Field)
			if !ok {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: fmt.Sprintf("champ inconnu (%s)", op.Field)})
				continue
			}
			// Le contrôle par champ : si la valeur d'avant est encore là,
			// personne n'a touché ce champ, la modification s'applique.
			if now := changes.JSON(f.get(current)); !create && now != op.Before {
				conflicts = append(conflicts, CommitConflict{
					Op:      op,
					Reason:  "ce champ a été modifié entre-temps",
					Current: now,
				})
				continue
			}
			if err := f.set(&work, json.RawMessage(op.After)); err != nil {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "valeur illisible"})
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

func commitTask(ctx context.Context, base Store, ops []changes.Op) ([]changes.Op, []CommitConflict, error) {
	id := ops[0].Target
	current, exists, err := base.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	work := current
	var applied []changes.Op
	var conflicts []CommitConflict
	create, remove := false, false

	for _, op := range ops {
		switch op.Action {
		case changes.Create:
			if exists {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "une tâche porte déjà cet identifiant"})
				continue
			}
			var created Task
			if err := json.Unmarshal([]byte(op.After), &created); err != nil {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "opération illisible"})
				continue
			}
			work, create = created, true
			applied = append(applied, op)
		case changes.Delete:
			if !exists {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "déjà supprimée"})
				continue
			}
			remove = true
			applied = append(applied, op)
		case changes.Set:
			if !exists && !create {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "supprimée entre-temps"})
				continue
			}
			f, ok := taskFieldByName(op.Field)
			if !ok {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: fmt.Sprintf("champ inconnu (%s)", op.Field)})
				continue
			}
			if now := changes.JSON(f.get(current)); !create && now != op.Before {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "ce champ a été modifié entre-temps", Current: now})
				continue
			}
			if err := f.set(&work, json.RawMessage(op.After)); err != nil {
				conflicts = append(conflicts, CommitConflict{Op: op, Reason: "valeur illisible"})
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
