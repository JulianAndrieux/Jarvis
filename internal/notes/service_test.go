package notes

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newTestService() (*Service, *FakeStore, *time.Time) {
	store := NewFakeStore()
	now := wednesday
	s := &Service{Store: store, Now: func() time.Time { return now }}
	return s, store, &now
}

func TestService_NoteLifecycle(t *testing.T) {
	s, _, now := newTestService()
	ctx := context.Background()
	n, err := s.NewNote(ctx, "doc-1")
	if err != nil || n.ID == "" || n.Title != "Sans titre" || len(n.DocIDs) != 1 || !n.CreatedAt.Equal(wednesday) {
		t.Fatalf("NewNote = %+v, %v", n, err)
	}
	*now = now.Add(time.Hour)
	saved, err := s.SaveNote(ctx, n.ID, "  Courses  ", "- lait", " maison, urgent ,, maison ", true)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Courses" || saved.Body != "- lait" || strings.Join(saved.Tags, "|") != "maison|urgent" || !saved.Pinned || !saved.UpdatedAt.Equal(*now) || !saved.CreatedAt.Equal(wednesday) || len(saved.DocIDs) != 1 {
		t.Errorf("SaveNote = %+v", saved)
	}
	if saved, _ := s.SaveNote(ctx, n.ID, " ", "", "", false); saved.Title != "Sans titre" {
		t.Errorf("empty title = %q", saved.Title)
	}
	if _, err := s.SaveNote(ctx, "absente", "x", "", "", false); err == nil {
		t.Error("SaveNote(unknown) = nil")
	}
}

func TestService_LinkAndUnlinkDocuments(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	for _, doc := range []string{"doc-1", "doc-2", "doc-1"} {
		if err := s.LinkDoc(ctx, n.ID, doc); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := store.GetNote(ctx, n.ID); strings.Join(got.DocIDs, ",") != "doc-1,doc-2" {
		t.Errorf("DocIDs = %v, want each document once", got.DocIDs)
	}
	s.UnlinkDoc(ctx, n.ID, "doc-1")
	if got, _, _ := store.GetNote(ctx, n.ID); strings.Join(got.DocIDs, ",") != "doc-2" {
		t.Errorf("after unlink = %v", got.DocIDs)
	}
}

// Supprimer une note garde ses tâches, déliées.
func TestService_DeleteNoteKeepsTasksUnlinked(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	task, _ := s.AddTask(ctx, "Acheter du lait", n.ID, "")
	if err := s.DeleteNote(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := store.GetTask(ctx, task.ID)
	if !ok || got.NoteID != "" {
		t.Errorf("task after note deletion = %+v, %v", got, ok)
	}
}

func TestService_AddTaskUsesQuickAdd(t *testing.T) {
	s, _, _ := newTestService()
	ctx := context.Background()
	task, err := s.AddTask(ctx, "Appeler le notaire demain !", "note-1", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" || task.Title != "Appeler le notaire" || task.Due != "2026-09-24" || task.Priority != High || task.NoteID != "note-1" || task.DocID != "doc-1" || !task.CreatedAt.Equal(wednesday) {
		t.Errorf("AddTask = %+v", task)
	}
	if _, err := s.AddTask(ctx, "   ", "", ""); err == nil {
		t.Error("empty task accepted")
	}
}

func TestService_ToggleTask(t *testing.T) {
	s, _, now := newTestService()
	ctx := context.Background()
	task, _ := s.AddTask(ctx, "Lire", "", "")
	*now = now.Add(time.Hour)
	done, err := s.ToggleTask(ctx, task.ID)
	if err != nil || !done.Done || !done.DoneAt.Equal(*now) {
		t.Fatalf("toggle = %+v, %v", done, err)
	}
	undone, _ := s.ToggleTask(ctx, task.ID)
	if undone.Done || !undone.DoneAt.IsZero() {
		t.Errorf("toggle back = %+v", undone)
	}
}

func TestService_SaveTaskValidatesDue(t *testing.T) {
	s, _, _ := newTestService()
	ctx := context.Background()
	task, _ := s.AddTask(ctx, "Lire", "", "")
	saved, err := s.SaveTask(ctx, task.ID, "Lire le rapport", "2026-10-01", "haute", "note-9", "doc-9")
	if err != nil || saved.Title != "Lire le rapport" || saved.Due != "2026-10-01" || saved.Priority != High || saved.NoteID != "note-9" || saved.DocID != "doc-9" {
		t.Fatalf("SaveTask = %+v, %v", saved, err)
	}
	for _, due := range []string{"01/10/2026", "2026-02-31"} {
		if _, err := s.SaveTask(ctx, task.ID, "x", due, "", "", ""); err == nil {
			t.Errorf("due %q accepted", due)
		}
	}
	if _, err := s.SaveTask(ctx, task.ID, " ", "", "", "", ""); err == nil {
		t.Error("empty title accepted")
	}
	if cleared, _ := s.SaveTask(ctx, task.ID, "x", "", "", "", ""); cleared.Due != "" || cleared.Priority != Normal {
		t.Errorf("cleared = %+v", cleared)
	}
}
