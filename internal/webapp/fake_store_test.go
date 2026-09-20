package webapp

import (
	"context"
	"testing"
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
