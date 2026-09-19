package bbox

import (
	"context"
	"errors"
	"testing"
)

func TestFakeExtractor_ReturnsConfiguredPages(t *testing.T) {
	want := []PageWords{{Page: 1, Width: 612, Height: 792, Words: []Word{{Text: "x"}}}}
	e := FakeExtractor{Pages: want}

	got, err := e.ExtractWords(context.Background(), "ignored.pdf")
	if err != nil {
		t.Fatalf("ExtractWords() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Page != 1 {
		t.Errorf("ExtractWords() = %+v, want %+v", got, want)
	}
}

func TestFakeExtractor_ReturnsConfiguredError(t *testing.T) {
	wantErr := errors.New("boom")
	e := FakeExtractor{Err: wantErr}

	_, err := e.ExtractWords(context.Background(), "ignored.pdf")
	if !errors.Is(err, wantErr) {
		t.Errorf("ExtractWords() error = %v, want %v", err, wantErr)
	}
}
