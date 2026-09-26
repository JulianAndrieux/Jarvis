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
	// Boxes, Archived : nombre de boîtes, dont archivées.
	Boxes    int
	Archived int
}

// BlockView : une boîte de note affichée. HTML est Text rendu en
// Markdown (entièrement échappé) ; Text sert à l'éditeur.
type BlockView struct {
	ID       string
	Text     string
	HTML     string
	Tags     []string
	Archived bool
	// ArchivedAt : heure locale, "" si la boîte n'est pas archivée.
	ArchivedAt string
	// TaskID, TaskTitle : la tâche née de cette boîte ("" : aucune).
	TaskID    string
	TaskTitle string
	// Editing : cette boîte est ouverte dans l'éditeur.
	Editing bool
}

// NoteBlocksView : la colonne de boîtes d'une note. C'est le seul
// fragment rendu par les actions de boîte — un refus (archive sans tag,
// boîte vide) arrive dans Error, avec le statut 200 : HTMX ne
// remplacerait pas la cible sur une réponse 4xx.
type NoteBlocksView struct {
	NoteID        string
	Blocks        []BlockView
	EditID        string
	ShowArchived  bool
	ArchivedCount int
	Error         string
}

// NotesFilters : recherche et dates saisies dans la liste des notes,
// telles que tapées. Error explique une date refusée (non appliquée).
type NotesFilters struct {
	Search string
	Tag    string
	From   string // AAAA-MM-JJ, inclus
	To     string // AAAA-MM-JJ, inclus
	Error  string
}

// Active : un filtre est en cours.
func (f NotesFilters) Active() bool {
	return f.Search != "" || f.Tag != "" || f.From != "" || f.To != ""
}

// notesFilterURL : lien d'une pastille de tag qui garde la recherche et
// les dates. url.Values échappe la recherche (un « & » la couperait).
func notesFilterURL(tag string, f NotesFilters) string {
	q := url.Values{}
	for k, v := range map[string]string{"tag": tag, "q": f.Search, "from": f.From, "to": f.To} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return "/notes"
	}
	return "/notes?" + q.Encode()
}

// blocksURL : le fragment de la colonne de boîtes, dans l'état courant
// (boîte ouverte, archives affichées ou non).
func blocksURL(v NoteBlocksView, editID string) string {
	return blocksState("/notes/"+v.NoteID+"/blocks", editID, v.ShowArchived)
}

// blockURL : l'action action sur une boîte ("" : l'enregistrer). L'état
// d'affichage suit, pour que le fragment revienne tel qu'il était.
func blockURL(v NoteBlocksView, blockID, action string) string {
	u := "/notes/" + v.NoteID + "/blocks/" + blockID
	if action != "" {
		u += "/" + action
	}
	return blocksState(u, "", v.ShowArchived)
}

func blocksState(u, editID string, showArchived bool) string {
	q := url.Values{}
	if editID != "" {
		q.Set("edit", editID)
	}
	if showArchived {
		q.Set("archives", "1")
	}
	if len(q) == 0 {
		return u
	}
	return u + "?" + q.Encode()
}

// mdTool : un bouton de la barre d'outils Markdown de l'éditeur. Key
// est lu par le script (data-md).
type mdTool struct{ Key, Label, Title string }

var mdTools = []mdTool{
	{"bold", "B", "Gras (⌘/Ctrl+B)"},
	{"italic", "I", "Italique (⌘/Ctrl+I)"},
	{"strike", "S", "Barré"},
	{"h", "H", "Titre"},
	{"ul", "•", "Liste à puces"},
	{"ol", "1.", "Liste numérotée"},
	{"code", "<>", "Code"},
	{"quote", "❝", "Citation"},
}

// boxCount : « 3 boîtes (1 archivée) » sur la carte d'une note.
func boxCount(c NoteCard) string {
	s := plural(c.Boxes, "boîte", "boîtes")
	if c.Archived > 0 {
		s += " (" + plural(c.Archived, "archivée", "archivées") + ")"
	}
	return s
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
	// MailID, MailSubject : l'email d'où vient la tâche (jalon 39).
	MailID      string
	MailSubject string
}

// TaskGroupView : un groupe de la todo.
type TaskGroupView struct {
	Bucket notes.Bucket
	Label  string
	Tasks  []TaskView
}

// NoteView : une note ouverte.
type NoteView struct {
	Note   notes.Note
	Blocks NoteBlocksView
	Docs   []DocLink
	Tasks  []TaskGroupView
	// Choices : documents proposés pour un nouveau lien.
	Choices []DocLink
	// MailSubject : l'objet de l'email d'où vient la note (Note.MailID).
	MailSubject string
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
