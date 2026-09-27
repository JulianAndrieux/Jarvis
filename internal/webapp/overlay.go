package webapp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Overlay met en attente ce qu'un humain modifie sur un document, et
// applique ces modifications à ses propres lectures — le changeset du
// jalon 48, étendu aux documents.
//
// Ce qui est mis en attente est volontairement étroit : les tags, le
// commentaire, et la suppression. Tout le reste passe directement.
//
//   - Les écritures de la machine (statut, avancement, résultat
//     d'extraction, texte de recherche, miniature) ne sont jamais mises en
//     attente : un commit qui retiendrait un résultat d'OCR serait absurde,
//     et le premier redémarrage le perdrait. Elles arrivent de toute façon
//     avec une portée de fond.
//   - Déposer un fichier et relancer une extraction sont des **actions**,
//     pas des modifications de données : elles ont des effets hors base
//     (fichier stocké, modèles sollicités) qu'on ne peut ni retenir ni
//     rejouer. Elles s'exécutent tout de suite.
//   - La suppression, elle, est bien une donnée : mise en attente, et les
//     fichiers ne sont purgés qu'au commit.
type Overlay struct {
	Base   Store
	Staged changes.ChangesetStore
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

func (o *Overlay) pending(ctx context.Context) ([]changes.Op, error) {
	return o.Staged.Pending(ctx, o.scope.Env, o.scope.User)
}

// --- Écritures mises en attente ---

func (o *Overlay) SetTags(ctx context.Context, id string, tags []string) error {
	return o.stageSet(ctx, id, "tags", func(j Job) any { return j.Tags }, tags)
}

func (o *Overlay) SetComment(ctx context.Context, id, comment string) error {
	return o.stageSet(ctx, id, "comment", func(j Job) any { return j.Comment }, comment)
}

// stageSet met en attente « ce champ prend cette valeur », si elle diffère
// de ce que je vois déjà.
func (o *Overlay) stageSet(ctx context.Context, id, field string, get func(Job) any, value any) error {
	base, ok, err := o.Base.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("webapp: document %s introuvable", id)
	}
	ops, err := o.pending(ctx)
	if err != nil {
		return err
	}
	seen, exists := applyJobOps(base, true, id, ops)
	if !exists {
		return fmt.Errorf("webapp: document %s introuvable", id)
	}
	if changes.JSON(get(seen)) == changes.JSON(value) {
		return nil
	}
	return o.Stager.Stage(ctx, changes.Op{
		Kind: changes.KindDocument, Target: id, Label: base.Filename, Field: field,
		Action: changes.Set, Before: changes.JSON(get(base)), After: changes.JSON(value),
		BaseVer: base.Version,
	})
}

func (o *Overlay) Delete(ctx context.Context, id string) error {
	job, ok, err := o.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("webapp: document %s introuvable", id)
	}
	return o.Stager.Stage(ctx, changes.Op{
		Kind: changes.KindDocument, Target: id, Label: job.Filename,
		Action: changes.Delete, BaseVer: job.Version,
	})
}

// --- Lectures, superposées ---

func (o *Overlay) Get(ctx context.Context, id string) (Job, bool, error) {
	job, ok, err := o.Base.Get(ctx, id)
	if err != nil {
		return Job{}, false, err
	}
	ops, err := o.pending(ctx)
	if err != nil {
		return Job{}, false, err
	}
	job, ok = applyJobOps(job, ok, id, ops)
	return job, ok, nil
}

// Count compte ce que je vois : la base, plus mes modifications en
// attente. Un document que je viens de supprimer ne compte plus pour moi,
// et il compte encore pour les autres.
func (o *Overlay) Count(ctx context.Context, q ListQuery) (int, error) {
	ops, err := o.pending(ctx)
	if err != nil {
		return 0, err
	}
	if len(ops) == 0 {
		return o.Base.Count(ctx, q) // rien en attente : le compte côté serveur
	}
	// Avec des modifications en attente, le compte doit passer par le même
	// prédicat que la liste — SummaryOnly : jamais les PDF pour compter.
	all, err := o.Base.List(ctx, ListQuery{SummaryOnly: true})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range all {
		if merged, ok := applyJobOps(j, true, j.ID, ops); ok && q.Matches(merged) {
			n++
		}
	}
	return n, nil
}

func (o *Overlay) List(ctx context.Context, q ListQuery) ([]Job, error) {
	ops, err := o.pending(ctx)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return o.Base.List(ctx, q) // rien en attente : le cas courant
	}
	// La requête est appliquée en Go, après mes opérations : un document
	// que je viens de retaguer doit sortir sur son nouveau tag. SummaryOnly
	// est conservé — il ne faut pas rapatrier les PDF pour filtrer.
	all, err := o.Base.List(ctx, ListQuery{SummaryOnly: q.SummaryOnly})
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	var out []Job
	for _, j := range all {
		merged, ok := applyJobOps(j, true, j.ID, ops)
		if !ok || !q.Matches(merged) {
			continue
		}
		out = append(out, merged)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// --- Tout le reste : la machine écrit directement ---

func (o *Overlay) Create(ctx context.Context, job Job) (Job, error) {
	return o.Base.Create(ctx, job)
}
func (o *Overlay) Update(ctx context.Context, job Job) error { return o.Base.Update(ctx, job) }
func (o *Overlay) SetThumbnail(ctx context.Context, id string, png []byte) error {
	return o.Base.SetThumbnail(ctx, id, png)
}
func (o *Overlay) SetProgress(ctx context.Context, id string, p *pipeline.Progress) error {
	return o.Base.SetProgress(ctx, id, p)
}
func (o *Overlay) WriteFile(ctx context.Context, id string, name FileName, data []byte) error {
	return o.Base.WriteFile(ctx, id, name, data)
}
func (o *Overlay) ReadFile(ctx context.Context, id string, name FileName) ([]byte, bool, error) {
	return o.Base.ReadFile(ctx, id, name)
}

// --- Application des opérations ---

type jobField struct {
	name string
	get  func(Job) any
	set  func(*Job, json.RawMessage) error
}

// jobFields : les champs d'un document qu'un humain modifie. Volontairement
// court — voir l'en-tête du type.
var jobFields = []jobField{
	{"tags", func(j Job) any { return j.Tags }, func(j *Job, v json.RawMessage) error { return json.Unmarshal(v, &j.Tags) }},
	{"comment", func(j Job) any { return j.Comment }, func(j *Job, v json.RawMessage) error { return json.Unmarshal(v, &j.Comment) }},
}

func jobFieldByName(name string) (jobField, bool) {
	for _, f := range jobFields {
		if f.name == name {
			return f, true
		}
	}
	return jobField{}, false
}

// applyJobOps superpose les opérations visant id. ok=false en sortie :
// « n'existe pas de mon point de vue » (inexistant, ou supprimé en
// attente).
func applyJobOps(base Job, exists bool, id string, ops []changes.Op) (Job, bool) {
	j := base
	for _, op := range ops {
		if op.Kind != changes.KindDocument || op.Target != id {
			continue
		}
		switch op.Action {
		case changes.Delete:
			exists = false
		case changes.Set:
			if !exists {
				continue
			}
			if f, ok := jobFieldByName(op.Field); ok {
				_ = f.set(&j, json.RawMessage(op.After))
			}
		}
	}
	return j, exists
}

// Applier applique les opérations en attente portant sur des documents.
type Applier struct {
	Base Store
}

func (a Applier) Kinds() []changes.Kind { return []changes.Kind{changes.KindDocument} }

func (a Applier) Apply(ctx context.Context, scope tenancy.Scope, ops []changes.Op) ([]changes.Op, []changes.Conflict, error) {
	base := a.Base.For(scope)
	id := ops[0].Target
	current, exists, err := base.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	var applied []changes.Op
	var conflicts []changes.Conflict
	remove := false
	// Les champs modifiés, dans l'ordre : une écriture ciblée par champ, pas
	// un remplacement du document — la leçon des jalons 23 et 39.
	set := map[string]json.RawMessage{}

	for _, op := range ops {
		switch op.Action {
		case changes.Delete:
			if !exists {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "déjà supprimé"})
				continue
			}
			remove = true
			applied = append(applied, op)
		case changes.Set:
			if !exists {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: "supprimé entre-temps"})
				continue
			}
			f, ok := jobFieldByName(op.Field)
			if !ok {
				conflicts = append(conflicts, changes.Conflict{Op: op, Reason: fmt.Sprintf("champ inconnu (%s)", op.Field)})
				continue
			}
			if now := changes.JSON(f.get(current)); !changes.FieldUnchanged(op, now) {
				conflicts = append(conflicts, changes.Conflict{Op: op,
					Reason: "ce champ a été modifié entre-temps", Current: now})
				continue
			}
			set[op.Field] = json.RawMessage(op.After)
			applied = append(applied, op)
		default:
			conflicts = append(conflicts, changes.Conflict{Op: op,
				Reason: fmt.Sprintf("un document ne se crée pas par un commit (%s)", op.Action)})
		}
	}

	if len(applied) == 0 {
		return nil, conflicts, nil
	}
	if remove {
		if err := base.Delete(ctx, id); err != nil {
			return nil, nil, err
		}
		return applied, conflicts, nil
	}
	for field, raw := range set {
		switch field {
		case "tags":
			var tags []string
			if err := json.Unmarshal(raw, &tags); err != nil {
				return nil, nil, fmt.Errorf("webapp: commit tags %s: %w", id, err)
			}
			if err := base.SetTags(ctx, id, tags); err != nil {
				return nil, nil, err
			}
		case "comment":
			var comment string
			if err := json.Unmarshal(raw, &comment); err != nil {
				return nil, nil, fmt.Errorf("webapp: commit comment %s: %w", id, err)
			}
			if err := base.SetComment(ctx, id, comment); err != nil {
				return nil, nil, err
			}
		}
	}
	return applied, conflicts, nil
}
