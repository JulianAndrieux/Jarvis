//go:build integration

package tickets

import (
	"context"
	"os"
	"testing"
	"time"
)

func newTestMongoStore(t *testing.T) *MongoStore {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "tickets_test")
	if err != nil {
		t.Fatalf("NewMongoStore() error = %v", err)
	}
	return s
}

// Le fil s'ajoute atomiquement ($push) et survit à un Update : l'agent
// écrit ses étapes pendant que l'utilisateur peut modifier le ticket.
func TestMongoStore_TicketLifecycle(t *testing.T) {
	s := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-ticket-" + time.Now().Format("150405.000000")
	defer s.Delete(ctx, id)

	tk := Ticket{ID: id, Title: "Filtre par date", Need: "Pouvoir filtrer les documents par date", Status: Draft, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"lit server.go", "cherche ListQuery"} {
		if err := s.AppendEvent(ctx, id, Event{At: time.Now(), Kind: EventStep, Author: AuthorAgent, Text: text, Detail: "sortie"}); err != nil {
			t.Fatal(err)
		}
	}
	tk.Status, tk.Plan = PlanReady, "1. ajouter ListQuery.Since"
	tk.Branch, tk.Diff, tk.Report = "ticket/"+id, "+\tSince string", "ok  tous les paquets"
	if err := s.Update(ctx, tk); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if got.Branch != "ticket/"+id || got.Diff != "+\tSince string" || got.Report == "" {
		t.Errorf("Get() branch %q diff %q report %q (jalon 28 fields not persisted)", got.Branch, got.Diff, got.Report)
	}
	if got.Status != PlanReady || got.Plan == "" || len(got.Events) != 2 || got.Events[1].Text != "cherche ListQuery" {
		t.Errorf("Get() = status %s plan %q events %+v", got.Status, got.Plan, got.Events)
	}
	list, err := s.List(ctx, PlanReady)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range list {
		found = found || l.ID == id
	}
	if !found {
		t.Error("List(PlanReady) does not include the ticket")
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, id); ok {
		t.Error("ticket still present after Delete")
	}
}
