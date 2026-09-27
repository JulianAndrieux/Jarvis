package tickets

import (
	"context"
	"testing"
	"time"
)

// Le cycle d'un ticket (jalon 26) : deux validations humaines — le plan
// de l'agent, puis le diff avant déploiement (décision de l'utilisateur).
func TestCanTransition(t *testing.T) {
	allowed := []struct{ from, to Status }{
		{Draft, Analyzing},
		{Analyzing, PlanReady},
		{Analyzing, Failed},
		{PlanReady, PlanApproved},
		{PlanReady, Analyzing}, // révision demandée
		{Failed, Analyzing},    // nouvelle tentative
		{Draft, Cancelled},
		{PlanReady, Cancelled},
		{Cancelled, Draft}, // ressuscité : remis en brouillon
	}
	for _, c := range allowed {
		if !CanTransition(c.from, c.to) {
			t.Errorf("CanTransition(%s -> %s) = false, want true", c.from, c.to)
		}
	}
	forbidden := []struct{ from, to Status }{
		{Draft, PlanApproved}, // pas de plan sans analyse
		{Analyzing, PlanApproved},
		{Cancelled, Analyzing}, // un ticket annulé est clos
		{Cancelled, Review},    // seul le brouillon se ressuscite
		{Deployed, Draft},      // un ticket déployé ne redevient pas un brouillon
		{PlanApproved, Draft},
	}
	for _, c := range forbidden {
		if CanTransition(c.from, c.to) {
			t.Errorf("CanTransition(%s -> %s) = true, want false", c.from, c.to)
		}
	}
}

func TestStatus_LabelAndActive(t *testing.T) {
	if Analyzing.Label() != "Analyse en cours" || PlanReady.Label() != "Plan à valider" {
		t.Errorf("labels = %q, %q", Analyzing.Label(), PlanReady.Label())
	}
	if !Analyzing.Active() || PlanReady.Active() {
		t.Error("only Analyzing is an active (agent working) status here")
	}
}

func TestFakeStore_CreateGetListAppendDelete(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	older := Ticket{ID: "a", Title: "Ancien", Status: Draft, CreatedAt: time.Now().Add(-time.Hour)}
	newer := Ticket{ID: "b", Title: "Récent", Status: PlanReady, CreatedAt: time.Now()}
	for _, tk := range []Ticket{older, newer} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	list, _ := s.List(ctx, "")
	if len(list) != 2 || list[0].ID != "b" {
		t.Errorf("List() = %+v, want newest first", list)
	}
	if only, _ := s.List(ctx, PlanReady); len(only) != 1 || only[0].ID != "b" {
		t.Errorf("List(PlanReady) = %+v", only)
	}

	ev := Event{At: time.Now(), Kind: EventComment, Author: AuthorUser, Text: "précision"}
	if err := s.AppendEvent(ctx, "a", ev); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.Get(ctx, "a")
	if !ok || len(got.Events) != 1 || got.Events[0].Text != "précision" {
		t.Errorf("Get() events = %+v", got.Events)
	}

	got.Status, got.Plan = Analyzing, "plan"
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "a")
	if got.Status != Analyzing || got.Plan != "plan" || len(got.Events) != 1 {
		t.Errorf("after Update: %+v (events must be preserved: they are append-only)", got)
	}

	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "a"); ok {
		t.Error("ticket still present after Delete")
	}
	if err := s.AppendEvent(ctx, "nope", ev); err == nil {
		t.Error("AppendEvent on unknown ticket: error = nil")
	}
}
