package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Tableau de bord (ticket "Revoir ordre des sections") : la première page
// de l'application — une statistique par section, dans l'ordre de la
// navigation, puis les derniers documents importés.
//
// « / » est aussi le contrôle de santé du lanceur (cmd/jarvis-launcher,
// 500 ms par tentative) et la première page de l'essai à blanc d'un
// déploiement : la page elle-même ne lit donc AUCUNE base — c'est un
// rendu statique, et les statistiques arrivent ensuite par HTMX
// (GET /dashboard/stats), comme le volet d'un document et la barre
// latérale. Une dizaine d'allers-retours Atlas avant le premier octet
// ferait expirer le contrôle de santé, et le lanceur tuerait une
// application pourtant saine.

// dashboardRecent : documents récents affichés.
const dashboardRecent = 6

// unreadable : ce qu'affiche une carte dont la source n'a pas pu être
// lue — jamais un « 0 », qui ferait croire à une base vide.
const (
	unreadable       = "—"
	unreadableDetail = "lecture impossible"
)

type docCounts struct{ Total, Active, Failed int }

type mailCounts struct{ Total, Reply, Untriaged int }

// dashboardData : ce que le tableau de bord lit, avant mise en forme —
// séparé de buildDashboard pour que la mise en forme reste une fonction
// pure, testable sans store ni horloge réelle.
type dashboardData struct {
	Docs   docCounts
	Recent []webapp.Job
	Notes  []notes.Note
	Tasks  []notes.Task
	// Tickets : tous les tickets connus (les statuts sont comptés ici).
	Tickets []tickets.Ticket
	Mails   mailCounts
	// NotesOn, MailOn, TicketsOn : services configurés. Un service absent
	// n'affiche pas sa carte — un « 0 » laisserait croire qu'on a compté.
	NotesOn, MailOn, TicketsOn bool
	// DocsErr, RecentErr, NotesErr, TasksErr, MailErr, TicketsErr : lecture
	// en échec. La carte le dit plutôt que d'afficher un zéro fabriqué
	// (« Documents : 0 » ferait croire que les documents ont disparu).
	DocsErr, RecentErr, NotesErr, TasksErr, MailErr, TicketsErr bool
}

// handleDashboard sert la page d'accueil : un rendu statique, sans aucune
// lecture de base (cf. le commentaire en tête de fichier).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, http.StatusOK, templates.DashboardPage(), "dashboard")
}

// handleDashboardStats sert les statistiques, chargées après la page.
// Chaque source est lue à part : une lecture en échec est journalisée et
// signalée sur sa carte, sans empêcher les autres de s'afficher.
func (s *Server) handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	var d dashboardData

	d.Docs.Total = dashRead(&d.DocsErr, "compte des documents", func() (int, error) {
		return s.Jobs.Count(ctx, webapp.ListQuery{})
	})
	for _, st := range []webapp.Status{webapp.StatusRunning, webapp.StatusPending} {
		d.Docs.Active += dashRead(&d.DocsErr, "documents "+string(st), func() (int, error) {
			return s.Jobs.Count(ctx, webapp.ListQuery{Status: st})
		})
	}
	d.Docs.Failed = dashRead(&d.DocsErr, "documents en échec", func() (int, error) {
		return s.Jobs.Count(ctx, webapp.ListQuery{Status: webapp.StatusFailed})
	})
	d.Recent = dashRead(&d.RecentErr, "derniers documents", func() ([]webapp.Job, error) {
		return s.Jobs.List(ctx, webapp.ListQuery{Limit: dashboardRecent, SummaryOnly: true})
	})

	if s.Notes != nil {
		d.NotesOn = true
		now = s.Notes.Clock()
		d.Notes = dashRead(&d.NotesErr, "notes", func() ([]notes.Note, error) {
			return s.Notes.Store.ListNotes(ctx, notes.NoteQuery{})
		})
		d.Tasks = dashRead(&d.TasksErr, "tâches", func() ([]notes.Task, error) {
			return s.Notes.Store.ListTasks(ctx, notes.TaskQuery{})
		})
	}
	if s.Mail != nil {
		d.MailOn = true
		d.Mails.Total = dashRead(&d.MailErr, "emails", func() (int, error) {
			return s.Mail.Store.Count(ctx, mail.Query{})
		})
		d.Mails.Reply = dashRead(&d.MailErr, "emails à répondre", func() (int, error) {
			return s.Mail.Store.Count(ctx, mail.Query{Reply: true})
		})
		d.Mails.Untriaged = dashRead(&d.MailErr, "emails en cours d'analyse", func() (int, error) {
			return s.Mail.Store.Count(ctx, mail.Query{Untriaged: true})
		})
	}
	if s.Tickets != nil {
		d.TicketsOn = true
		d.Tickets = dashRead(&d.TicketsErr, "tickets", func() ([]tickets.Ticket, error) {
			return s.Tickets.Store.List(ctx, "")
		})
	}

	w.Header().Set("Cache-Control", "no-store")
	renderPage(w, r, http.StatusOK, templates.DashboardStats(buildDashboard(now, d)), "dashboard stats")
}

// dashRead exécute une lecture du tableau de bord : une erreur est
// journalisée (comme renderPage) et marque sa source, jamais avalée en
// silence — la carte dira « lecture impossible » au lieu de zéro.
func dashRead[T any](failed *bool, what string, read func() (T, error)) T {
	v, err := read()
	if err != nil {
		*failed = true
		fmt.Fprintf(os.Stderr, "jarvisapp: dashboard: %s: %v\n", what, err)
		var zero T
		return zero
	}
	return v
}

// buildDashboard met en forme les cartes (dans l'ordre de la navigation)
// et la liste des derniers documents.
func buildDashboard(now time.Time, d dashboardData) templates.DashboardView {
	var v templates.DashboardView

	if d.NotesOn {
		c := templates.StatCard{Icon: "📝", Label: "Notes", Href: "/notes"}
		if d.NotesErr {
			c.Value, c.Detail = unreadable, unreadableDetail
		} else {
			pinned := 0
			for _, n := range d.Notes {
				if n.Pinned {
					pinned++
				}
			}
			cut := len(d.Notes) >= notes.MaxListNotes
			c.Value = atLeast(len(d.Notes), cut)
			c.Detail = details(count(pinned, "épinglée", "épinglées"), truncated(cut))
		}
		v.Cards = append(v.Cards, c)
	}
	if d.MailOn {
		c := templates.StatCard{Icon: "📧", Label: "Emails", Href: "/emails"}
		if d.MailErr {
			c.Value, c.Detail = unreadable, unreadableDetail
		} else {
			c.Value = fmt.Sprint(d.Mails.Total)
			c.Detail = details(
				count(d.Mails.Reply, "à répondre", "à répondre"),
				count(d.Mails.Untriaged, "en cours d'analyse", "en cours d'analyse"),
			)
			c.Alert = d.Mails.Reply > 0
		}
		v.Cards = append(v.Cards, c)
	}
	docs := templates.StatCard{Icon: "📄", Label: "Documents", Href: "/documents"}
	if d.DocsErr {
		docs.Value, docs.Detail = unreadable, unreadableDetail
	} else {
		docs.Value = fmt.Sprint(d.Docs.Total)
		docs.Detail = details(
			count(d.Docs.Active, "en traitement", "en traitement"),
			count(d.Docs.Failed, "en échec", "en échec"),
		)
		docs.Alert = d.Docs.Failed > 0
	}
	v.Cards = append(v.Cards, docs)
	if d.NotesOn {
		c := templates.StatCard{Icon: "✅", Label: "Tâches", Href: "/tasks"}
		if d.TasksErr {
			c.Value, c.Detail = unreadable, unreadableDetail
		} else {
			// Les groupes de la todo, jamais une comparaison de dates réécrite
			// ici (notes.GroupTasks est la seule à décider ce qui est en retard).
			var open, overdue, today int
			for _, g := range notes.GroupTasks(d.Tasks, now) {
				switch g.Bucket {
				case notes.Overdue:
					overdue = len(g.Tasks)
				case notes.Today:
					today = len(g.Tasks)
				}
				if g.Bucket != notes.Done {
					open += len(g.Tasks)
				}
			}
			// Liste tronquée : les groupes eux-mêmes ne portent que sur ce
			// qui a été ramené (les plus anciennes tâches d'abord).
			cut := len(d.Tasks) >= notes.MaxListTasks
			c.Value = atLeast(open, cut)
			c.Detail = details(
				count(overdue, "en retard", "en retard"),
				count(today, "aujourd'hui", "aujourd'hui"),
				truncated(cut),
			)
			c.Alert = overdue > 0
		}
		v.Cards = append(v.Cards, c)
	}
	if d.TicketsOn {
		c := templates.StatCard{Icon: "🎫", Label: "Tickets", Href: "/tickets"}
		if d.TicketsErr {
			c.Value, c.Detail = unreadable, unreadableDetail
		} else {
			var open, toValidate, running, failed int
			for _, t := range d.Tickets {
				switch t.Status {
				case tickets.Deployed, tickets.Cancelled:
					continue
				case tickets.PlanReady, tickets.Review:
					toValidate++
				case tickets.Analyzing, tickets.Developing, tickets.Deploying:
					running++
				case tickets.Failed:
					failed++
				}
				open++
			}
			cut := len(d.Tickets) >= tickets.MaxList
			c.Value = atLeast(open, cut)
			c.Detail = details(
				count(toValidate, "à valider", "à valider"),
				count(running, "en cours", "en cours"),
				count(failed, "en échec", "en échec"),
				truncated(cut),
			)
			c.Alert = failed > 0
		}
		v.Cards = append(v.Cards, c)
	}

	v.RecentErr = d.RecentErr
	for _, j := range d.Recent {
		v.Recent = append(v.Recent, documentRow(j, "02/01 15:04"))
	}
	return v
}

// atLeast : « 42+ » quand le compte vient d'une liste tronquée par son
// store — un minimum, jamais un nombre faux présenté comme exact.
func atLeast(n int, cut bool) string {
	if cut {
		return fmt.Sprintf("%d+", n)
	}
	return fmt.Sprint(n)
}

// truncated : la mention à joindre à un compte tiré d'une liste tronquée.
func truncated(cut bool) string {
	if cut {
		return "liste tronquée"
	}
	return ""
}

// count : « 3 en échec » — chaîne vide si n vaut zéro (rien à signaler ne
// s'affiche pas du tout).
func count(n int, singular, plural string) string {
	switch {
	case n == 0:
		return ""
	case n == 1:
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// details assemble les sous-totaux non vides d'une carte.
func details(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}
