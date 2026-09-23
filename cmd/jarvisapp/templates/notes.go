package templates

import (
	"net/url"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/notes"
)

// Notes et tâches (jalon 32) : vues préparées par cmd/jarvisapp.

// DocLink : un document de la bibliothèque, par son nom.
type DocLink struct {
	ID   string
	Name string
}

// NoteCard : une note dans la liste.
type NoteCard struct {
	ID      string
	Title   string
	Excerpt string
	Tags    []string
	Pinned  bool
	Updated string
}

// TaskView : une tâche affichée.
type TaskView struct {
	ID        string
	Title     string
	Done      bool
	Due       string // "AAAA-MM-JJ"
	DueLabel  string // lisible (aujourd'hui, demain, lundi, 30/09)
	Overdue   bool
	Priority  notes.Priority
	NoteID    string
	NoteTitle string
	DocID     string
	DocName   string
}

// TaskGroupView : un groupe de la todo.
type TaskGroupView struct {
	Bucket notes.Bucket
	Label  string
	Tasks  []TaskView
}

// NoteView : une note ouverte.
type NoteView struct {
	Note     notes.Note
	BodyHTML string // Markdown rendu, entièrement échappé
	Docs     []DocLink
	Tasks    []TaskGroupView
	Edit     bool
	// Choices : documents proposés pour un nouveau lien.
	Choices []DocLink
}

// TaskEditView : le formulaire d'une tâche.
type TaskEditView struct {
	Task  notes.Task
	Notes []DocLink // notes proposées (même forme : identifiant + titre)
	Docs  []DocLink
	Error string
}

// DocumentLinksView : notes et tâches liées à un document.
type DocumentLinksView struct {
	DocID string
	Notes []NoteCard
	Tasks []TaskGroupView
}

// quickAddHelp : la syntaxe de la saisie rapide.
const quickAddHelp = "En fin de saisie : aujourd'hui, demain, lundi…, 30/09 ou 30/09/2026 pour l'échéance ; ! pour une priorité haute, !basse pour une basse."

func urlQuery(s string) string { return url.QueryEscape(s) }

func joinTags(tags []string) string { return strings.Join(tags, ", ") }
