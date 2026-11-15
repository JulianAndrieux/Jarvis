package notes

import (
	"context"
	"testing"
	"time"
)

// NewTask crée une tâche à partir de champs explicites : contrairement à
// la saisie rapide, un titre qui finit par « demain » reste un titre.
func TestService_NewTask_ExplicitFields(t *testing.T) {
	s := &Service{Store: NewFakeStore(), Now: func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }}
	ctx := context.Background()
	got, err := s.NewTask(ctx, TaskInput{Title: "  Rappeler le plombier demain ", Due: "2026-11-20", Priority: "haute", MailID: "m1", DocID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Rappeler le plombier demain" || got.Due != "2026-11-20" || got.Priority != High || got.MailID != "m1" || got.DocID != "d1" {
		t.Errorf("tâche = %+v", got)
	}
	stored, ok, _ := s.DB(ctx).GetTask(ctx, got.ID)
	if !ok || stored.Title != got.Title || stored.CreatedAt.IsZero() {
		t.Errorf("enregistrée = %+v, %v", stored, ok)
	}
}

func TestService_NewTask_RefusesEmptyTitleAndBadDue(t *testing.T) {
	s := &Service{Store: NewFakeStore()}
	ctx := context.Background()
	if _, err := s.NewTask(ctx, TaskInput{Title: "   "}); err == nil {
		t.Error("titre vide accepté")
	}
	for _, due := range []string{"20/11/2026", "2026-02-31", "demain"} {
		if _, err := s.NewTask(ctx, TaskInput{Title: "x", Due: due}); err == nil {
			t.Errorf("échéance %q acceptée", due)
		}
	}
	if l, _ := s.DB(ctx).ListTasks(ctx, TaskQuery{}); len(l) != 0 {
		t.Errorf("une tâche refusée a été enregistrée : %v", l)
	}
}
