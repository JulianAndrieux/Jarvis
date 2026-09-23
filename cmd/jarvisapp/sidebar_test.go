package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Jalon 34 : barre latérale de l'application — tâches de la semaine,
// traitements en cours, erreurs à suivre.

func TestBuildSidebar_WeekTasks(t *testing.T) {
	tasks := []notes.Task{
		{ID: "retard", Title: "En retard", Due: "2026-09-20"},
		{ID: "auj", Title: "Aujourd'hui", Due: "2026-09-23", Priority: notes.High},
		{ID: "j7", Title: "Dans 7 jours", Due: "2026-09-30"},
		{ID: "j8", Title: "Dans 8 jours", Due: "2026-10-01"},
		{ID: "sans", Title: "Sans échéance"},
		{ID: "faite", Title: "Faite", Due: "2026-09-24", Done: true},
	}
	v := buildSidebar(notesNow, tasks, nil, nil, nil)
	var ids []string
	for _, t := range v.Week {
		ids = append(ids, t.ID)
	}
	if got := strings.Join(ids, ","); got != "retard,auj,j7" {
		t.Errorf("week = %s, want overdue, today, within 7 days (not done, not undated, not later)", got)
	}
	if !v.Week[0].Overdue || v.Week[1].DueLabel != "aujourd'hui" {
		t.Errorf("week[0] = %+v, week[1] = %+v", v.Week[0], v.Week[1])
	}
}

func TestBuildSidebar_LimitsWithOverflowCount(t *testing.T) {
	var tasks []notes.Task
	for i := 0; i < sidebarMax+3; i++ {
		tasks = append(tasks, notes.Task{ID: fmt.Sprint(i), Title: "t", Due: "2026-09-24"})
	}
	v := buildSidebar(notesNow, tasks, nil, nil, nil)
	if len(v.Week) != sidebarMax || v.WeekMore != 3 {
		t.Errorf("week = %d (+%d), want %d (+3)", len(v.Week), v.WeekMore, sidebarMax)
	}
}

func TestBuildSidebar_RunningAndErrors(t *testing.T) {
	active := []webapp.Job{
		{ID: "j1", Filename: "scan.pdf", Status: webapp.StatusRunning},
		{ID: "j2", Filename: "attente.docx", Status: webapp.StatusPending},
	}
	failed := []webapp.Job{{ID: "j3", Filename: "casse.pdf", Status: webapp.StatusFailed, Err: "vlm: délai dépassé\nstack trace"}}
	tks := []tickets.Ticket{
		{ID: "t1", Title: "Analyse", Status: tickets.Analyzing},
		{ID: "t2", Title: "À relire", Status: tickets.Review},
		{ID: "t3", Title: "Cassé", Status: tickets.Failed},
		{ID: "t4", Title: "Fini", Status: tickets.Deployed},
		{ID: "t5", Title: "Brouillon", Status: tickets.Draft},
	}
	v := buildSidebar(notesNow, nil, active, failed, tks)

	running := map[string]string{}
	for _, it := range v.Running {
		running[it.Href] = it.Detail
	}
	for href, detail := range map[string]string{
		"/documents/j1": "Traitement en cours",
		"/documents/j2": "En attente",
		"/tickets/t1":   "Analyse en cours",
		"/tickets/t2":   "À valider : Diff à valider",
	} {
		if running[href] != detail {
			t.Errorf("running[%s] = %q, want %q", href, running[href], detail)
		}
	}
	if len(v.Running) != 4 {
		t.Errorf("running = %+v, want finished and draft tickets left out", v.Running)
	}
	errs := map[string]string{}
	for _, it := range v.Errors {
		errs[it.Href] = it.Detail
	}
	if errs["/documents/j3"] != "vlm: délai dépassé" || errs["/tickets/t3"] == "" || len(v.Errors) != 2 {
		t.Errorf("errors = %+v, want the failed document (first line of its error) and ticket", v.Errors)
	}
	// Le message complet reste lisible au survol (la cause est souvent à
	// la fin : « source file could not be loaded »).
	for _, it := range v.Errors {
		if it.Href == "/documents/j3" && it.Full != "vlm: délai dépassé\nstack trace" {
			t.Errorf("full error = %q", it.Full)
		}
	}
}

func TestSidebar_FragmentAndLayout(t *testing.T) {
	s, jobs := newTestServer(t, &blockingRunner{})
	store := notes.NewFakeStore()
	s.Notes = &notes.Service{Store: store, Now: func() time.Time { return notesNow }}
	s.Tickets = &tickets.Manager{Store: tickets.NewFakeStore()}
	s.Notes.AddTask(context.Background(), "Payer <b>EDF</b> demain", "", "")
	jobs.Create(context.Background(), webapp.Job{ID: "bad", Filename: "casse.pdf", Status: webapp.StatusFailed, Err: "boom", CreatedAt: notesNow})

	body := do(s, http.MethodGet, "/sidebar", nil).Body.String()
	for _, want := range []string{"Cette semaine", "Payer &lt;b&gt;EDF&lt;/b&gt;", "demain", "En cours", "Erreurs à suivre", `href="/documents/bad"`, "boom", `href="/tasks"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sidebar lacks %q", want)
		}
	}
	// Barre présente dans l'application, absente de l'Admin.
	if !strings.Contains(do(s, http.MethodGet, "/tasks", nil).Body.String(), `hx-get="/sidebar"`) {
		t.Error("user pages do not load the sidebar")
	}
	s.model = newCodeTestServer().model
	if strings.Contains(do(s, http.MethodGet, "/admin/classes", nil).Body.String(), `hx-get="/sidebar"`) {
		t.Error("admin pages load the sidebar")
	}
}

// Cocher une tâche dans la barre renvoie la barre à jour, sans la tâche.
func TestSidebar_ToggleTask(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	store := notes.NewFakeStore()
	s.Notes = &notes.Service{Store: store, Now: func() time.Time { return notesNow }}
	task, _ := s.Notes.AddTask(context.Background(), "Appeler le notaire demain", "", "")
	rec := do(s, http.MethodPost, "/sidebar/tasks/"+task.ID+"/toggle", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Appeler le notaire") || !strings.Contains(rec.Body.String(), "Cette semaine") {
		t.Errorf("toggle: %d, body still lists the task or is not the sidebar", rec.Code)
	}
	if tk, _, _ := store.GetTask(context.Background(), task.ID); !tk.Done {
		t.Error("task not done")
	}
}
