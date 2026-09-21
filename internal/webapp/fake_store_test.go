package webapp

import (
	"context"
	"testing"
	"time"
)

func TestFakeStore_CreateThenGet(t *testing.T) {
	s := NewFakeStore()
	job := Job{ID: "1", Filename: "doc.pdf", Status: StatusPending}

	created, err := s.Create(context.Background(), job)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if created.ID != "1" {
		t.Errorf("created.ID = %q, want 1", created.ID)
	}

	got, ok, err := s.Get(context.Background(), "1")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got.Filename != "doc.pdf" {
		t.Errorf("got.Filename = %q, want doc.pdf", got.Filename)
	}
}

func TestFakeStore_Get_UnknownID_ReturnsFalse(t *testing.T) {
	s := NewFakeStore()

	_, ok, err := s.Get(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if ok {
		t.Error("Get() ok = true, want false")
	}
}

func TestFakeStore_Update_ExistingJob_Overwrites(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	if _, err := s.Create(ctx, Job{ID: "1", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, Job{ID: "1", Status: StatusDone}); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}

	got, ok, err := s.Get(ctx, "1")
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	if got.Status != StatusDone {
		t.Errorf("got.Status = %q, want done", got.Status)
	}
}

func TestFakeStore_Update_UnknownJob_ReturnsError(t *testing.T) {
	s := NewFakeStore()

	err := s.Update(context.Background(), Job{ID: "does-not-exist"})
	if err == nil {
		t.Fatal("Update() error = nil, want non-nil for a job that was never created")
	}
}

func TestFakeStore_List_SortedByCreatedAtDescending(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	base := time.Now()
	_, _ = s.Create(ctx, Job{ID: "old", CreatedAt: base})
	_, _ = s.Create(ctx, Job{ID: "newest", CreatedAt: base.Add(2 * time.Hour)})
	_, _ = s.Create(ctx, Job{ID: "middle", CreatedAt: base.Add(1 * time.Hour)})

	got, err := s.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 3 || got[0].ID != "newest" || got[1].ID != "middle" || got[2].ID != "old" {
		ids := make([]string, len(got))
		for i, j := range got {
			ids[i] = j.ID
		}
		t.Errorf("List() ids = %v, want [newest middle old]", ids)
	}
}

func TestFakeStore_List_FiltersBySearchOnFilenameDocTypeAndTags_CaseInsensitive(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	_, _ = s.Create(ctx, Job{ID: "1", Filename: "Facture-Acme.pdf", DocType: "facture"})
	_, _ = s.Create(ctx, Job{ID: "2", Filename: "rapport.pdf", DocType: "piece_identite", Tags: []string{"Urgent"}})
	_, _ = s.Create(ctx, Job{ID: "3", Filename: "autre.pdf", DocType: "facture"})

	byFilename, err := s.List(ctx, ListQuery{Search: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byFilename) != 1 || byFilename[0].ID != "1" {
		t.Errorf("search by filename = %+v, want just job 1", byFilename)
	}

	byTag, err := s.List(ctx, ListQuery{Search: "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTag) != 1 || byTag[0].ID != "2" {
		t.Errorf("search by tag = %+v, want just job 2", byTag)
	}

	byDocType, err := s.List(ctx, ListQuery{Search: "facture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byDocType) != 2 {
		t.Errorf("search by doc_type = %+v, want 2 matches", byDocType)
	}
}

func TestFakeStore_List_RespectsLimit(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = s.Create(ctx, Job{ID: string(rune('a' + i)), CreatedAt: time.Now()})
	}

	got, err := s.List(ctx, ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len(List()) = %d, want 2 (Limit)", len(got))
	}
}
