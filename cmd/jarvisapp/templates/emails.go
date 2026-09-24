package templates

import (
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/mail"
)

// Emails (jalon 39) : vues préparées par cmd/jarvisapp.

// MailRow : un email dans la liste.
type MailRow struct {
	ID          string
	From        string
	Subject     string
	Summary     string
	Date        string
	Category    mail.Category
	Unread      bool
	Attachments int
	TriageError bool
}

// MailFilter : un filtre par catégorie.
type MailFilter struct {
	Label  string
	Href   string
	Active bool
}

// MailListView : la boîte de réception.
type MailListView struct {
	Status  mail.Status
	Rows    []MailRow
	Search  string
	Filters []MailFilter
	// Category : le filtre en cours ("" : tous), gardé par la recherche.
	Category string
}

// MailAttachmentView : une pièce jointe.
type MailAttachmentView struct {
	Index    int
	Filename string
	Size     string
	Stored   bool
	DocID    string
	DocName  string
}

// MailView : un email ouvert.
type MailView struct {
	Mail mail.Mail
	To   string
	Cc   string
	Date string
	// TaskInput : la tâche proposée (l'action suggérée, sinon l'objet).
	TaskInput   string
	Attachments []MailAttachmentView
	Tasks       []TaskGroupView
	Notes       []NoteCard
}

// MailSettingsView : la configuration de la boîte.
type MailSettingsView struct {
	Host       string
	User       string
	Configured bool
	Error      string
}

func categoryChip(c mail.Category) string { return "chip chip-cat-" + string(c) }

func mailStatusLine(st mail.Status) string {
	if st.Running {
		return "relève en cours…"
	}
	if st.LastSync.IsZero() {
		return "pas encore relevée"
	}
	line := "relevée à " + st.LastSync.Local().Format("15:04")
	if st.LastNew > 0 {
		line += fmt.Sprintf(" (%d nouveaux)", st.LastNew)
	}
	return line
}

// SizeLabel : une taille lisible (octets, Ko, Mo).
func SizeLabel(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f Mo", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d Ko", n>>10)
	}
	return fmt.Sprintf("%d octets", n)
}
