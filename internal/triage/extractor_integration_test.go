//go:build integration

// Ce fichier n'est compilé/exécuté qu'avec `go test -tags=integration`. Il
// dépend du binaire externe `pdftotext` (poppler-utils) et n'est donc pas
// exécuté par défaut — cohérent avec la contrainte "aucun test ne doit
// avoir besoin d'un GPU" et, plus généralement, aucune dépendance système
// forte dans la suite de tests par défaut.
package triage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPdftotextExtractor_NativePDF(t *testing.T) {
	e := PdftotextExtractor{}

	pages, err := e.ExtractPerPage(context.Background(), filepath.Join("..", "..", "testdata", "fixtures", "native.pdf"))
	if err != nil {
		t.Fatalf("ExtractPerPage() error = %v, want nil", err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	if !strings.Contains(pages[0].Text, "Facture") {
		t.Errorf("page text = %q, want it to contain %q", pages[0].Text, "Facture")
	}
}

func TestPdftotextExtractor_ScannedPDF(t *testing.T) {
	e := PdftotextExtractor{}

	pages, err := e.ExtractPerPage(context.Background(), filepath.Join("..", "..", "testdata", "fixtures", "scanned.pdf"))
	if err != nil {
		t.Fatalf("ExtractPerPage() error = %v, want nil", err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	if strings.TrimSpace(pages[0].Text) != "" {
		t.Errorf("page text = %q, want empty (no text layer)", pages[0].Text)
	}
}

func TestPdftotextExtractor_ScannedContentPDF(t *testing.T) {
	// scanned_content.pdf est une image JPEG (contenu réel, rendu de
	// native.pdf) intégrée sans aucune couche texte — contrairement à
	// scanned.pdf (page vide), elle a du contenu visuel à transcrire, mais
	// doit tout de même être détectée comme sans texte natif.
	e := PdftotextExtractor{}

	pages, err := e.ExtractPerPage(context.Background(), filepath.Join("..", "..", "testdata", "fixtures", "scanned_content.pdf"))
	if err != nil {
		t.Fatalf("ExtractPerPage() error = %v, want nil", err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	if strings.TrimSpace(pages[0].Text) != "" {
		t.Errorf("page text = %q, want empty (image only, no text layer)", pages[0].Text)
	}
}

func TestPdftotextExtractor_MixedPDF(t *testing.T) {
	e := PdftotextExtractor{}

	pages, err := e.ExtractPerPage(context.Background(), filepath.Join("..", "..", "testdata", "fixtures", "mixed.pdf"))
	if err != nil {
		t.Fatalf("ExtractPerPage() error = %v, want nil", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d, want 2", len(pages))
	}
	if !strings.Contains(pages[0].Text, "Correspondance") {
		t.Errorf("page 1 text = %q, want it to contain %q", pages[0].Text, "Correspondance")
	}
	if strings.TrimSpace(pages[1].Text) != "" {
		t.Errorf("page 2 text = %q, want empty", pages[1].Text)
	}
}

func TestPdftotextExtractor_NonExistentFile(t *testing.T) {
	e := PdftotextExtractor{}

	_, err := e.ExtractPerPage(context.Background(), "does-not-exist.pdf")
	if err == nil {
		t.Fatal("ExtractPerPage() error = nil, want non-nil for a missing file")
	}
}
