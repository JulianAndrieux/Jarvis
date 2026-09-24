package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
)

// Emails (jalon 39) : la boîte de réception relevée par mail.Service,
// triée par le modèle local ; tâches, notes et documents depuis un email.

func (s *Server) emailRoutes(r chi.Router) {
	r.Get("/emails", s.handleEmails)
	r.Post("/emails/sync", s.handleEmailSync)
	r.Get("/emails/settings", s.handleEmailSettings)
	r.Post("/emails/settings", s.handleEmailSettingsSave)
	r.Get("/emails/{id}", s.handleEmail)
	r.Post("/emails/{id}/retriage", s.handleEmailRetriage)
	r.Post("/emails/{id}/task", s.handleEmailTask)
	r.Post("/emails/{id}/note", s.handleEmailNote)
	r.Get("/emails/{id}/attachments/{index}", s.handleEmailAttachment)
	r.Post("/emails/{id}/attachments/{index}/import", s.handleEmailAttachmentImport)
}

func (s *Server) mailEnabled(w http.ResponseWriter) bool {
	if s.Mail == nil {
		http.Error(w, "emails non configurés", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Server) handleEmails(w http.ResponseWriter, r *http.Request) {
	if !s.mailEnabled(w) {
		return
	}
	search, cat := r.URL.Query().Get("q"), r.URL.Query().Get("cat")
	list, err := s.Mail.Store.List(r.Context(), mail.Query{Search: search, Category: mail.Category(cat)})
	if err != nil {
		serverError(w, err)
		return
	}
	v := templates.MailListView{Status: s.Mail.Status(), Search: search, Category: cat}
	filter := func(label, value string) templates.MailFilter {
		q := url.Values{}
		if search != "" {
			q.Set("q", search)
		}
		if value != "" {
			q.Set("cat", value)
		}
		href := "/emails"
		if len(q) > 0 {
			href += "?" + q.Encode()
		}
		return templates.MailFilter{Label: label, Href: href, Active: cat == value}
	}
	v.Filters = append(v.Filters, filter("Tous", ""))
	for _, c := range mail.Categories {
		v.Filters = append(v.Filters, filter(mail.CategoryLabel(c), string(c)))
	}
	for _, m := range list {
		from := m.From.Name
		if from == "" {
			from = m.From.Email
		}
		v.Rows = append(v.Rows, templates.MailRow{
			ID: m.ID, From: from, Subject: m.Subject, Summary: m.Triage.Summary,
			Date: m.Date.Local().Format("02/01 15:04"), Category: m.Triage.Category,
			Unread: !m.Seen, Attachments: len(m.Attachments), TriageError: m.Triage.Error != "",
		})
	}
	renderPage(w, r, http.StatusOK, templates.EmailsPage(v), "emails")
}

func (s *Server) handleEmailSync(w http.ResponseWriter, r *http.Request) {
	if !s.mailEnabled(w) {
		return
	}
	s.Mail.Kick()
	http.Redirect(w, r, "/emails", http.StatusSeeOther)
}

func (s *Server) handleEmailSettings(w http.ResponseWriter, r *http.Request) {
	if !s.mailEnabled(w) {
		return
	}
	c, ok, err := s.Mail.Config()
	if err != nil {
		serverError(w, err)
		return
	}
	if c.Host == "" {
		c.Host = mail.DefaultHost
	}
	// Le mot de passe enregistré n'est jamais renvoyé à la page.
	renderPage(w, r, http.StatusOK, templates.EmailSettingsPage(templates.MailSettingsView{Host: c.Host, User: c.User, Configured: ok}), "email settings")
}

func (s *Server) handleEmailSettingsSave(w http.ResponseWriter, r *http.Request) {
	if !s.mailEnabled(w) {
		return
	}
	saved, ok, _ := s.Mail.Config()
	c := mail.Config{Host: r.FormValue("host"), User: r.FormValue("user"), Password: r.FormValue("password")}
	// Champ laissé vide sur la même adresse : le mot de passe enregistré.
	if c.Password == "" && ok && strings.TrimSpace(c.User) == saved.User {
		c.Password = saved.Password
	}
	if err := s.Mail.Configure(r.Context(), c); err != nil {
		v := templates.MailSettingsView{Host: c.Normalize().Host, User: c.User, Configured: ok, Error: err.Error()}
		renderPage(w, r, http.StatusUnprocessableEntity, templates.EmailSettingsPage(v), "email settings")
		return
	}
	http.Redirect(w, r, "/emails", http.StatusSeeOther)
}

// email : l'email de l'URL ; false (réponse déjà écrite) sinon.
func (s *Server) email(w http.ResponseWriter, r *http.Request) (mail.Mail, bool) {
	if !s.mailEnabled(w) {
		return mail.Mail{}, false
	}
	m, ok, err := s.Mail.Store.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		serverError(w, err)
		return mail.Mail{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return mail.Mail{}, false
	}
	return m, true
}

func (s *Server) handleEmail(w http.ResponseWriter, r *http.Request) {
	m, ok := s.email(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v := templates.MailView{
		Mail: m, To: joinAddresses(m.To), Cc: joinAddresses(m.Cc),
		Date: m.Date.Local().Format("02/01/2006 15:04"), TaskInput: m.Triage.Action,
	}
	if v.TaskInput == "" {
		v.TaskInput = m.Subject
	}
	var docIDs []string
	for _, a := range m.Attachments {
		if a.DocID != "" {
			docIDs = append(docIDs, a.DocID)
		}
	}
	names := s.docNames(ctx, docIDs)
	for _, a := range m.Attachments {
		v.Attachments = append(v.Attachments, templates.MailAttachmentView{
			Index: a.Index, Filename: a.Filename, Size: templates.SizeLabel(a.Size),
			Stored: a.Stored, DocID: a.DocID, DocName: names[a.DocID],
		})
	}
	if s.Notes != nil {
		if tasks, err := s.Notes.Store.ListTasks(ctx, notes.TaskQuery{MailID: m.ID}); err == nil {
			v.Tasks = s.taskGroups(ctx, tasks, taskContext{mailID: m.ID})
		}
		if ns, err := s.Notes.Store.ListNotes(ctx, notes.NoteQuery{MailID: m.ID}); err == nil {
			v.Notes = noteCards(ns)
		}
	}
	renderPage(w, r, http.StatusOK, templates.EmailPage(v), "email")
}

func joinAddresses(list []mail.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

func (s *Server) handleEmailRetriage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.email(w, r)
	if !ok {
		return
	}
	if err := s.Mail.Retriage(r.Context(), m.ID); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/emails/"+m.ID, http.StatusSeeOther)
}

func (s *Server) handleEmailTask(w http.ResponseWriter, r *http.Request) {
	m, ok := s.email(w, r)
	if !ok || !s.notesEnabled(w) {
		return
	}
	if _, err := s.Notes.AddMailTask(r.Context(), r.FormValue("input"), m.ID); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/emails/"+m.ID, http.StatusSeeOther)
}

// maxQuoted : le texte de l'email recopié dans une note, au plus.
const maxQuoted = 3000

func (s *Server) handleEmailNote(w http.ResponseWriter, r *http.Request) {
	m, ok := s.email(w, r)
	if !ok || !s.notesEnabled(w) {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Email de %s, le %s.\n\n", m.From, m.Date.Local().Format("02/01/2006 15:04"))
	if m.Triage.Summary != "" {
		b.WriteString(m.Triage.Summary + "\n\n")
	}
	text := []rune(strings.TrimSpace(m.Text))
	if len(text) > maxQuoted {
		text = append(text[:maxQuoted], '…')
	}
	for _, line := range strings.Split(string(text), "\n") {
		b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
	}
	n, err := s.Notes.NewMailNote(r.Context(), m.ID, m.Subject, b.String())
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/notes/"+n.ID, http.StatusSeeOther)
}

// attachment : la pièce jointe de l'URL et son contenu ; false (réponse
// déjà écrite) sinon — une pièce jointe non copiée (trop grosse) est
// introuvable ici.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request) (mail.Mail, mail.Attachment, []byte, bool) {
	m, ok := s.email(w, r)
	if !ok {
		return m, mail.Attachment{}, nil, false
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	for _, a := range m.Attachments {
		if err == nil && a.Index == index && a.Stored {
			data, found, err := s.Mail.Store.Attachment(r.Context(), m.ID, index)
			if err != nil {
				serverError(w, err)
				return m, a, nil, false
			}
			if found {
				return m, a, data, true
			}
		}
	}
	http.Error(w, "pièce jointe introuvable (trop volumineuse pour avoir été copiée ?)", http.StatusNotFound)
	return m, mail.Attachment{}, nil, false
}

// handleEmailAttachment sert une pièce jointe en téléchargement, jamais
// affichée par le navigateur (contenu fourni par un inconnu).
func (s *Server) handleEmailAttachment(w http.ResponseWriter, r *http.Request) {
	_, a, data, ok := s.attachment(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", a.Filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// handleEmailAttachmentImport envoie une pièce jointe dans la
// bibliothèque (classification, extraction), une seule fois.
func (s *Server) handleEmailAttachmentImport(w http.ResponseWriter, r *http.Request) {
	m, a, data, ok := s.attachment(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if a.DocID != "" {
		if _, exists, _ := s.Jobs.Get(ctx, a.DocID); exists {
			http.Redirect(w, r, "/emails/"+m.ID, http.StatusSeeOther)
			return
		}
	}
	name := a.Filename
	if name == "" {
		name = fmt.Sprintf("piece-jointe-%d", a.Index)
	}
	job, err := s.Jobs.Submit(context.WithoutCancel(ctx), name, data)
	if err != nil {
		serverError(w, err)
		return
	}
	s.Jobs.SetTags(ctx, job.ID, []string{"email"})
	if err := s.Mail.Store.SetAttachmentDoc(ctx, m.ID, a.Index, job.ID); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/emails/"+m.ID, http.StatusSeeOther)
}

// mailSubjects : l'objet de chaque email demandé (vide si introuvable).
func (s *Server) mailSubjects(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if s.Mail == nil {
		return out
	}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		if m, ok, err := s.Mail.Store.Get(ctx, id); err == nil && ok {
			out[id] = m.Subject
		} else {
			out[id] = "email"
		}
	}
	return out
}
