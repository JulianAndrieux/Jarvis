package vlm

import (
	"context"
	"errors"
	"testing"
)

func TestFakeClient_ReturnsConfiguredResultForPage(t *testing.T) {
	want := ParseResult{
		Markdown: "# Facture\n\nTotal: 123.45 EUR",
		Model:    ModelInfo{Name: "test-vlm", Version: "0.0.0"},
		Prompt:   "extract layout as markdown",
	}
	f := &FakeClient{Results: map[int]ParseResult{2: want}}

	got, err := f.ParsePage(context.Background(), PageImage{Page: 2, PNG: []byte("fake-png")})
	if err != nil {
		t.Fatalf("ParsePage() error = %v, want nil", err)
	}
	if got != want {
		t.Errorf("ParsePage() = %+v, want %+v", got, want)
	}
}

func TestFakeClient_UnconfiguredPage_ReturnsError(t *testing.T) {
	f := &FakeClient{Results: map[int]ParseResult{1: {}}}

	_, err := f.ParsePage(context.Background(), PageImage{Page: 5, PNG: []byte("x")})
	if err == nil {
		t.Fatal("ParsePage() error = nil, want non-nil for a page with no configured result")
	}
}

func TestFakeClient_ConfiguredError_TakesPrecedence(t *testing.T) {
	wantErr := errors.New("boom")
	f := &FakeClient{
		Results: map[int]ParseResult{1: {Markdown: "should not be returned"}},
		Err:     wantErr,
	}

	_, err := f.ParsePage(context.Background(), PageImage{Page: 1, PNG: []byte("x")})
	if !errors.Is(err, wantErr) {
		t.Errorf("ParsePage() error = %v, want %v", err, wantErr)
	}
}

func TestFakeClient_RecordsCalls(t *testing.T) {
	f := &FakeClient{Results: map[int]ParseResult{1: {}, 2: {}}}

	img1 := PageImage{Page: 1, PNG: []byte("a")}
	img2 := PageImage{Page: 2, PNG: []byte("b")}
	if _, err := f.ParsePage(context.Background(), img1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ParsePage(context.Background(), img2); err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 2 {
		t.Fatalf("len(Calls) = %d, want 2", len(f.Calls))
	}
	if f.Calls[0].Page != img1.Page || string(f.Calls[0].PNG) != string(img1.PNG) {
		t.Errorf("Calls[0] = %+v, want %+v", f.Calls[0], img1)
	}
	if f.Calls[1].Page != img2.Page || string(f.Calls[1].PNG) != string(img2.PNG) {
		t.Errorf("Calls[1] = %+v, want %+v", f.Calls[1], img2)
	}
}
