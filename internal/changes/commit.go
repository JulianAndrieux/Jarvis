package changes

import (
	"context"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Conflict : une opération en attente qui ne peut pas s'appliquer, avec de
// quoi l'expliquer et trancher.
type Conflict struct {
	Op Op
	// Reason, en français, dit ce qui s'est passé.
	Reason string
	// Current : la valeur du champ telle qu'elle est aujourd'hui (JSON),
	// pour montrer les deux côtés comme une revue de diff.
	Current string
}

// CommitResult : ce qu'un commit a fait. Les opérations en conflit restent
// en attente — rien n'est appliqué en silence par-dessus la modification de
// quelqu'un d'autre.
type CommitResult struct {
	Applied   int
	Conflicts []Conflict
}

// Applier sait appliquer les opérations d'un ou plusieurs genres
// d'entités. Un paquet qui possède un type (notes, documents) fournit le
// sien : le commit reste générique, la connaissance des champs reste là où
// elle a un sens.
type Applier interface {
	// Kinds : les genres d'entités que cet Applier traite.
	Kinds() []Kind
	// Apply applique les opérations visant une même entité, dans l'ordre.
	// Rend celles qui ont été appliquées (à retirer du changeset) et celles
	// en conflit (à laisser en attente). Une erreur signale une panne de
	// persistance, pas un conflit.
	Apply(ctx context.Context, scope tenancy.Scope, ops []Op) (applied []Op, conflicts []Conflict, err error)
}

// Committer applique le changeset d'un user en dispatchant chaque entité à
// l'Applier de son genre.
type Committer struct {
	Staged   ChangesetStore
	Appliers []Applier
}

// Commit applique tout ce qui peut l'être, entité par entité, dans l'ordre
// où les modifications ont été faites.
func (c *Committer) Commit(ctx context.Context, scope tenancy.Scope) (CommitResult, error) {
	var res CommitResult
	if err := scope.Valid(); err != nil {
		return res, err
	}
	ops, err := c.Staged.Pending(ctx, scope.Env, scope.User)
	if err != nil {
		return res, err
	}
	byKind := map[Kind]Applier{}
	for _, a := range c.Appliers {
		for _, k := range a.Kinds() {
			byKind[k] = a
		}
	}

	order, groups := groupByTarget(ops)
	for _, key := range order {
		group := groups[key]
		applier, ok := byKind[key.kind]
		if !ok {
			// Un genre sans Applier : l'opération reste en attente plutôt que
			// d'être perdue, et on dit pourquoi.
			for _, op := range group {
				res.Conflicts = append(res.Conflicts, Conflict{Op: op,
					Reason: fmt.Sprintf("aucun mécanisme pour appliquer une modification de %s", key.kind)})
			}
			continue
		}
		applied, conflicts, err := applier.Apply(ctx, scope, group)
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
	kind   Kind
	target string
}

// groupByTarget regroupe par entité en conservant l'ordre d'apparition :
// une modification peut en suivre une autre sur le même champ.
func groupByTarget(ops []Op) ([]targetKey, map[targetKey][]Op) {
	groups := map[targetKey][]Op{}
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

// FieldUnchanged : le contrôle de conflit, au grain du champ. Une
// modification s'applique si la valeur d'avant qu'elle a enregistrée est
// encore celle de la base — donc si personne n'a touché ce champ-là.
func FieldUnchanged(op Op, currentJSON string) bool { return currentJSON == op.Before }
