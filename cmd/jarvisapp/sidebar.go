package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Barre latérale de l'application (jalon 34) : tâches de la semaine,
// emails à traiter (jalon 39), traitements en cours, erreurs à suivre. Chargée à part par chaque page
// de l'application et rafraîchie toute seule.

// sidebarMax : éléments affichés par bloc (au-delà : « + N »).
const sidebarMax = 8

// weekDays : horizon du bloc « Cette semaine ».
const weekDays = 7

func (s *Server) handleSidebar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	var tasks []notes.Task
	if s.Notes != nil {
		now = s.Notes.Clock()
		tasks, _ = s.Notes.Store.ListTasks(ctx, notes.TaskQuery{})
	}
	var active, failed []webapp.Job
	for _, st := range []webapp.Status{webapp.StatusRunning, webapp.StatusPending} {
		jobs, _ := s.Jobs.List(ctx, webapp.ListQuery{Status: st, SummaryOnly: true})
		active = append(active, jobs...)
	}
	failed, _ = s.Jobs.List(ctx, webapp.ListQuery{Status: webapp.StatusFailed, SummaryOnly: true, Limit: 50})
	var tks []tickets.Ticket
	if s.Tickets != nil {
		tks, _ = s.Tickets.Store.List(ctx, "")
	}
	var mails []mail.Mail
	if s.Mail != nil {
		mails, _ = s.Mail.Store.List(ctx, mail.Query{Category: mail.Action, Limit: 50})
	}
	// Une source illisible laisse son bloc vide plutôt que de casser la
	// barre (affichée sur toutes les pages).
	v := buildSidebar(now, tasks, active, failed, tks, mails)
	w.Header().Set("Cache-Control", "no-store")
	renderPage(w, r, http.StatusOK, templates.Sidebar(v), "sidebar")
}

// handleSidebarToggle coche une tâche depuis la barre, et la renvoie à
// jour.
func (s *Server) handleSidebarToggle(w http.ResponseWriter, r *http.Request) {
	if !s.notesEnabled(w) {
		return
	}
	if _, err := s.Notes.ToggleTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.handleSidebar(w, r)
}

// buildSidebar choisit ce que la barre affiche.
func buildSidebar(now time.Time, tasks []notes.Task, active, failed []webapp.Job, tks []tickets.Ticket, mails []mail.Mail) templates.SidebarView {
	var v templates.SidebarView

	// Emails à traiter de la semaine, sauf ceux déjà devenus une tâche.
	handled := map[string]bool{}
	for _, t := range tasks {
		if t.MailID != "" {
			handled[t.MailID] = true
		}
	}
	for _, m := range mails {
		if m.Triage.Category != mail.Action || handled[m.ID] || m.Date.Before(now.AddDate(0, 0, -weekDays)) || len(v.Mails) == sidebarMax {
			continue
		}
		from := m.From.Name
		if from == "" {
			from = m.From.Email
		}
		v.Mails = append(v.Mails, templates.SideItem{Href: "/emails/" + m.ID, Icon: "📧", Title: m.Subject, Detail: from, Full: m.Triage.Summary})
	}

	horizon := notes.DateOf(now.AddDate(0, 0, weekDays))
	var week []notes.Task
	for _, t := range tasks {
		if !t.Done && t.Due != "" && t.Due <= horizon {
			week = append(week, t)
		}
	}
	for _, g := range notes.GroupTasks(week, now) {
		for _, t := range g.Tasks {
			if len(v.Week) == sidebarMax {
				v.WeekMore++
				continue
			}
			v.Week = append(v.Week, templates.TaskView{
				ID: t.ID, Title: t.Title, Due: t.Due, Priority: t.Priority,
				DueLabel: notes.DueLabel(t.Due, now), Overdue: g.Bucket == notes.Overdue,
			})
		}
	}

	for _, j := range active {
		detail := "Traitement en cours"
		if j.Status == webapp.StatusPending {
			detail = "En attente"
		}
		v.Running = append(v.Running, templates.SideItem{Href: "/documents/" + j.ID, Icon: "📄", Title: j.Filename, Detail: detail})
	}
	for _, t := range tks {
		item := templates.SideItem{Href: "/tickets/" + t.ID, Icon: "🎫", Title: t.Title}
		switch t.Status {
		case tickets.Analyzing, tickets.Developing, tickets.Deploying:
			item.Detail = t.Status.Label()
			v.Running = append(v.Running, item)
		case tickets.PlanReady, tickets.Review:
			item.Detail = "À valider : " + t.Status.Label()
			v.Running = append(v.Running, item)
		case tickets.Failed:
			item.Detail = t.Status.Label()
			v.Errors = append(v.Errors, item)
		}
	}
	for _, j := range failed {
		v.Errors = append(v.Errors, templates.SideItem{Href: "/documents/" + j.ID, Icon: "📄", Title: j.Filename, Detail: firstLine(j.Err, 90), Full: j.Err})
	}
	if len(v.Running) > sidebarMax {
		v.Running = v.Running[:sidebarMax]
	}
	if len(v.Errors) > sidebarMax {
		v.Errors = v.Errors[:sidebarMax]
	}
	return v
}

// firstLine : la première ligne d'un message d'erreur, bornée.
func firstLine(s string, max int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
