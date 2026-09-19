package triage

import (
	"context"
	"errors"
	"testing"
)

func TestFakeExtractor_ReturnsConfiguredPages(t *testing.T) {
	want := []PageText{{Page: 1, Text: "hello"}}
	e := FakeExtractor{Pages: want}

	got, err := e.ExtractPerPage(context.Background(), "ignored.pdf")
	if err != nil {
		t.Fatalf("ExtractPerPage() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("ExtractPerPage() = %+v, want %+v", got, want)
	}
}

func TestFakeExtractor_ReturnsConfiguredError(t *testing.T) {
	wantErr := errors.New("boom")
	e := FakeExtractor{Err: wantErr}

	_, err := e.ExtractPerPage(context.Background(), "ignored.pdf")
	if !errors.Is(err, wantErr) {
		t.Errorf("ExtractPerPage() error = %v, want %v", err, wantErr)
	}
}
