//go:build integration

// Dépend du binaire externe `pdftotext` (poppler-utils) — voir
// internal/triage/extractor_integration_test.go pour la justification du
// build tag.
package bbox

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPdftotextBBoxExtractor_NativePDF(t *testing.T) {
	e := PdftotextBBoxExtractor{}

	pages, err := e.ExtractWords(context.Background(), filepath.Join("..", "..", "testdata", "fixtures", "native.pdf"))
	if err != nil {
		t.Fatalf("ExtractWords() error = %v, want nil", err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	p := pages[0]
	if p.Page != 1 {
		t.Errorf("Page = %d, want 1", p.Page)
	}
	if p.Width != 612 || p.Height != 792 {
		t.Errorf("Width/Height = %v/%v, want 612/792", p.Width, p.Height)
	}
	if len(p.Words) == 0 {
		t.Fatal("Words is empty, want the fixture's words")
	}
	if p.Words[0].Text != "Facture" {
		t.Errorf("Words[0].Text = %q, want %q", p.Words[0].Text, "Facture")
	}
	if p.Words[0].XMin <= 0 || p.Words[0].XMax <= p.Words[0].XMin {
		t.Errorf("Words[0] bbox looks invalid: %+v", p.Words[0])
	}
}

func TestPdftotextBBoxExtractor_MixedPDF_EmptyPageHasNoWords(t *testing.T) {
	e := PdftotextBBoxExtractor{}

	pages, err := e.ExtractWords(context.Background(), filepath.Join("..", "..", "testdata", "fixtures", "mixed.pdf"))
	if err != nil {
		t.Fatalf("ExtractWords() error = %v, want nil", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d, want 2", len(pages))
	}
	if len(pages[0].Words) == 0 {
		t.Error("page 1 Words is empty, want the fixture's words")
	}
	if len(pages[1].Words) != 0 {
		t.Errorf("page 2 Words = %+v, want empty (blank page)", pages[1].Words)
	}
}

func TestPdftotextBBoxExtractor_NonExistentFile(t *testing.T) {
	e := PdftotextBBoxExtractor{}

	_, err := e.ExtractWords(context.Background(), "does-not-exist.pdf")
	if err == nil {
		t.Fatal("ExtractWords() error = nil, want non-nil for a missing file")
	}
}
