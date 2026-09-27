// Package notes est la prise de notes et la todo de Jarvis (jalon 32) :
// des notes en Markdown et des tâches à échéance, liées entre elles et
// aux documents de la bibliothèque. Stockées dans MongoDB Atlas, comme
// les documents (décision de l'utilisateur, cf. CLAUDE.md).
package notes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Note est une note en Markdown.
type Note struct {
	ID string `bson:"_id"`
	// Env : l'environnement propriétaire. Renseigné par le Store, jamais
	// par l'appelant.
	Env   tenancy.EnvID `bson:"env_id"`
	Title string        `bson:"title"`
	Body  string        `bson:"body"`
	Tags  []string      `bson:"tags"`
	// Pinned : affichée en tête de liste.
	Pinned bool `bson:"pinned"`
	// DocIDs : documents de la bibliothèque liés à la note.
	DocIDs []string `bson:"doc_ids"`
	// MailID : l'email d'où vient la note ("" : aucun ; jalon 39).
	MailID    string    `bson:"mail_id"`
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
	// Version monte à chaque écriture. Une mise à jour porte la version
	// qu'elle a lue : si elle ne correspond plus, c'est que quelqu'un a
	// écrit entre-temps, et l'écriture est refusée (ErrConflict) plutôt que
	// d'effacer sa modification. C'est aussi l'ancrage du rebase du
	// changeset (jalon 48).
	Version int `bson:"version"`
}

// Priority d'une tâche.
type Priority string

const (
	Low    Priority = "basse"
	Normal Priority = "" // valeur par défaut
	High   Priority = "haute"
)

// Label : libellé affiché.
func (p Priority) Label() string {
	switch p {
	case High:
		return "haute"
	case Low:
		return "basse"
	}
	return "normale"
}

// rank : ordre de tri (la plus urgente d'abord).
func (p Priority) rank() int {
	switch p {
	case High:
		return 0
	case Low:
		return 2
	}
	return 1
}

// ParsePriority lit une priorité saisie ("haute", "basse", sinon normale).
func ParsePriority(s string) Priority {
	switch Priority(s) {
	case High, Low:
		return Priority(s)
	}
	return Normal
}

// Task est une tâche de la todo.
type Task struct {
	ID string `bson:"_id"`
	// Env : l'environnement propriétaire, renseigné par le Store.
	Env   tenancy.EnvID `bson:"env_id"`
	Title string        `bson:"title"`
	// Due : échéance "AAAA-MM-JJ" ("" : sans échéance). Une date, pas un
	// instant : aucune question de fuseau horaire, et l'ordre
	// alphabétique est l'ordre chronologique.
	Due      string   `bson:"due"`
	Priority Priority `bson:"priority"`
	Done     bool     `bson:"done"`
	// NoteID, DocID, MailID : note, document et email liés ("" : aucun).
	NoteID    string    `bson:"note_id"`
	DocID     string    `bson:"doc_id"`
	MailID    string    `bson:"mail_id"`
	CreatedAt time.Time `bson:"created_at"`
	DoneAt    time.Time `bson:"done_at"`
	// Version : voir Note.Version.
	Version int `bson:"version"`
}

// NoteQuery filtre une liste de notes.
type NoteQuery struct {
	// Search : sous-chaîne (insensible à la casse) du titre, du texte ou
	// d'un tag.
	Search string
	Tag    string
	DocID  string
	MailID string
}

// TaskQuery filtre une liste de tâches.
type TaskQuery struct {
	NoteID string
	DocID  string
	MailID string
}

// ErrConflict : l'entité a changé depuis sa lecture. Jamais un écrasement
// silencieux — même règle que « les valeurs sous le seuil sont marquées
// pour revue humaine, jamais acceptées silencieusement ».
var ErrConflict = errors.New("notes: modifiée entre-temps")

// Conflict porte l'état courant, pour que l'interface puisse montrer
// « quelqu'un a modifié ceci pendant ta saisie » côte à côte plutôt que
// d'annoncer un échec sans recours.
type Conflict struct {
	Current Note
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("la note %q a été modifiée entre-temps", c.Current.Title)
}

func (c *Conflict) Unwrap() error { return ErrConflict }

// Store persiste notes et tâches (MongoStore en production, FakeStore en
// test). Get : ok=false si l'élément n'existe pas ; Update et Delete
// échouent sur un élément inconnu.
type Store interface {
	// For rend la même persistance vue depuis un environnement : toutes
	// les lectures et écritures de la vue rendue portent sur ce seul
	// environnement. Un store sans portée refuse toute opération.
	For(scope tenancy.Scope) Store
	CreateNote(ctx context.Context, n Note) error
	GetNote(ctx context.Context, id string) (Note, bool, error)
	// UpdateNote réécrit la note. Conditionnel : n.Version doit être celle
	// enregistrée, sinon ErrConflict et rien n'est écrit. La nouvelle
	// version est un incrément.
	UpdateNote(ctx context.Context, n Note) error
	DeleteNote(ctx context.Context, id string) error
	// ListNotes : épinglées d'abord, puis la plus récemment modifiée.
	ListNotes(ctx context.Context, q NoteQuery) ([]Note, error)

	CreateTask(ctx context.Context, t Task) error
	GetTask(ctx context.Context, id string) (Task, bool, error)
	// UpdateTask : même contrat conditionnel qu'UpdateNote.
	UpdateTask(ctx context.Context, t Task) error
	DeleteTask(ctx context.Context, id string) error
	ListTasks(ctx context.Context, q TaskQuery) ([]Task, error)
}

// Matches dit si une note satisfait la requête — hors cloisonnement par
// environnement (c'est la portée du store qui s'en charge), hors tri.
//
// En Go, comme webapp.ListQuery.Matches, et pour la même raison : la fake
// l'utilise, MongoStore exprime le même filtre en $regex, le contrat commun
// aux deux surveille qu'ils s'accordent, et l'overlay du changeset
// (jalon 48) doit appliquer la même requête à des notes modifiées en
// mémoire, que Mongo ne peut pas filtrer.
func (q NoteQuery) Matches(n Note) bool {
	if q.Tag != "" && !slices.Contains(n.Tags, q.Tag) {
		return false
	}
	if q.DocID != "" && !slices.Contains(n.DocIDs, q.DocID) {
		return false
	}
	if q.MailID != "" && n.MailID != q.MailID {
		return false
	}
	if q.Search == "" {
		return true
	}
	return noteMatches(n, strings.ToLower(q.Search))
}

// Matches : même rôle pour une tâche.
func (q TaskQuery) Matches(t Task) bool {
	if q.NoteID != "" && t.NoteID != q.NoteID {
		return false
	}
	if q.DocID != "" && t.DocID != q.DocID {
		return false
	}
	return q.MailID == "" || t.MailID == q.MailID
}

// SortNotes : épinglées d'abord, puis la plus récemment modifiée — l'ordre
// que tout Store doit rendre, réutilisable par l'overlay après
// réapplication des opérations en attente.
func SortNotes(out []Note) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}

// SortTasks : de la plus ancienne à la plus récente (l'ordre de la todo).
func SortTasks(out []Task) {
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
}
