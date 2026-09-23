// Package notes est la prise de notes et la todo de Jarvis (jalon 32) :
// des notes en Markdown et des tâches à échéance, liées entre elles et
// aux documents de la bibliothèque. Stockées dans MongoDB Atlas, comme
// les documents (décision de l'utilisateur, cf. CLAUDE.md).
package notes

import (
	"context"
	"time"
)

// Note est une note en Markdown.
type Note struct {
	ID    string   `bson:"_id"`
	Title string   `bson:"title"`
	Body  string   `bson:"body"`
	Tags  []string `bson:"tags"`
	// Pinned : affichée en tête de liste.
	Pinned bool `bson:"pinned"`
	// DocIDs : documents de la bibliothèque liés à la note.
	DocIDs    []string  `bson:"doc_ids"`
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
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
	ID    string `bson:"_id"`
	Title string `bson:"title"`
	// Due : échéance "AAAA-MM-JJ" ("" : sans échéance). Une date, pas un
	// instant : aucune question de fuseau horaire, et l'ordre
	// alphabétique est l'ordre chronologique.
	Due      string   `bson:"due"`
	Priority Priority `bson:"priority"`
	Done     bool     `bson:"done"`
	// NoteID, DocID : note et document liés ("" : aucun).
	NoteID    string    `bson:"note_id"`
	DocID     string    `bson:"doc_id"`
	CreatedAt time.Time `bson:"created_at"`
	DoneAt    time.Time `bson:"done_at"`
}

// NoteQuery filtre une liste de notes.
type NoteQuery struct {
	// Search : sous-chaîne (insensible à la casse) du titre, du texte ou
	// d'un tag.
	Search string
	Tag    string
	DocID  string
}

// TaskQuery filtre une liste de tâches.
type TaskQuery struct {
	NoteID string
	DocID  string
}

// Store persiste notes et tâches (MongoStore en production, FakeStore en
// test). Get : ok=false si l'élément n'existe pas ; Update et Delete
// échouent sur un élément inconnu.
type Store interface {
	CreateNote(ctx context.Context, n Note) error
	GetNote(ctx context.Context, id string) (Note, bool, error)
	UpdateNote(ctx context.Context, n Note) error
	DeleteNote(ctx context.Context, id string) error
	// ListNotes : épinglées d'abord, puis la plus récemment modifiée.
	ListNotes(ctx context.Context, q NoteQuery) ([]Note, error)

	CreateTask(ctx context.Context, t Task) error
	GetTask(ctx context.Context, id string) (Task, bool, error)
	UpdateTask(ctx context.Context, t Task) error
	DeleteTask(ctx context.Context, id string) error
	ListTasks(ctx context.Context, q TaskQuery) ([]Task, error)
}
