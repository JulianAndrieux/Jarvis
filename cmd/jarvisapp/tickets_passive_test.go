package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Instance hébergée (rôle cloud) : on crée et on suit les tickets, le
// travail se fait sur le Mac.
func TestTickets_PassiveInstance_CreatesAndExplainsWhoWorks(t *testing.T) {
	s, store := newTicketServer(t, "p")
	s.Tickets.Passive = true
	s.Tickets.DefaultAgent = tickets.AgentClaude

	list := get(t, s, "/tickets").Body.String()
	if !strings.Contains(list, "pris par l'instance du Mac") {
		t.Errorf("tickets page does not say who works on the tickets")
	}
	if !strings.Contains(list, `value="claude"`) {
		t.Errorf("agent choice missing on a passive instance")
	}

	rec := postForm(t, s, "/tickets", url.Values{"title": {"Filtre"}, "need": {"besoin"}, "agent": {"claude"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	all, _ := store.List(context.Background(), "")
	if len(all) != 1 || all[0].Status != tickets.Draft || all[0].Agent != tickets.AgentClaude {
		t.Fatalf("tickets = %+v, want one draft for Claude", all)
	}
	id := all[0].ID

	page := get(t, s, "/tickets/"+id).Body.String()
	if strings.Contains(page, "/analyze") {
		t.Errorf("a passive instance must not offer to start the analysis")
	}
	if !strings.Contains(page, "instance du Mac") {
		t.Errorf("ticket page does not explain where the work happens")
	}
	if !strings.Contains(page, `hx-post="/tickets/`+id+`/cancel"`) {
		t.Errorf("a draft should still be cancellable from here")
	}
	// Même forgée, l'action est refusée.
	if rec := postForm(t, s, "/tickets/"+id+"/analyze", nil); rec.Code != http.StatusConflict {
		t.Errorf("forged analyze = %d, want 409", rec.Code)
	}
}

// Le travail fait ailleurs se suit en direct, sans bouton d'action.
func TestTickets_PassiveInstance_FollowsWorkDoneElsewhere(t *testing.T) {
	s, store := newTicketServer(t, "p")
	s.Tickets.Passive = true
	store.Create(context.Background(), tickets.Ticket{ID: "t1", Title: "T", Status: tickets.Developing, CreatedAt: time.Now()})
	store.Create(context.Background(), tickets.Ticket{ID: "t2", Title: "U", Status: tickets.Review, Branch: "ticket/t2", Diff: "diff --git a/x.go b/x.go\n+ligne", CreatedAt: time.Now()})

	running := get(t, s, "/tickets/t1").Body.String()
	if !strings.Contains(running, `hx-get="/tickets/t1/panel"`) {
		t.Errorf("a ticket worked elsewhere should still refresh itself")
	}
	review := get(t, s, "/tickets/t2").Body.String()
	for _, absent := range []string{"/tickets/t2/accept", "/tickets/t2/changes", "/tickets/t2/cancel"} {
		if strings.Contains(review, absent) {
			t.Errorf("passive review page offers %s", absent)
		}
	}
	if !strings.Contains(review, "ligne") {
		t.Errorf("the diff should still be readable from here")
	}
}
