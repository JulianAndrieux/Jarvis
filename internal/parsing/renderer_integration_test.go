//go:build integration

// Dépend du binaire externe `pdftoppm` (poppler-utils) — voir
// internal/triage/extractor_integration_test.go pour la justification du
// build tag.
package parsing

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestPdftoppmRenderer_RendersRealPNG(t *testing.T) {
	r := PdftoppmRenderer{}
	path := filepath.Join("..", "..", "testdata", "fixtures", "native.pdf")

	png, err := r.RenderPage(context.Background(), path, 1, 100)
	if err != nil {
		t.Fatalf("RenderPage() error = %v, want nil", err)
	}
	if len(png) == 0 {
		t.Fatal("RenderPage() returned no bytes")
	}
	if !bytes.HasPrefix(png, pngMagic) {
		t.Errorf("RenderPage() output does not start with the PNG magic bytes")
	}
}

func TestPdftoppmRenderer_MixedPDF_BothPages(t *testing.T) {
	r := PdftoppmRenderer{}
	path := filepath.Join("..", "..", "testdata", "fixtures", "mixed.pdf")

	for _, page := range []int{1, 2} {
		png, err := r.RenderPage(context.Background(), path, page, 100)
		if err != nil {
			t.Fatalf("RenderPage(page=%d) error = %v, want nil", page, err)
		}
		if !bytes.HasPrefix(png, pngMagic) {
			t.Errorf("RenderPage(page=%d) output does not start with the PNG magic bytes", page)
		}
	}
}

func TestPdftoppmRenderer_NonExistentFile(t *testing.T) {
	r := PdftoppmRenderer{}

	_, err := r.RenderPage(context.Background(), "does-not-exist.pdf", 1, 100)
	if err == nil {
		t.Fatal("RenderPage() error = nil, want non-nil for a missing file")
	}
}

func TestPdftoppmRenderer_OutOfRangePage(t *testing.T) {
	r := PdftoppmRenderer{}
	path := filepath.Join("..", "..", "testdata", "fixtures", "native.pdf") // 1 page

	_, err := r.RenderPage(context.Background(), path, 99, 100)
	if err == nil {
		t.Fatal("RenderPage() error = nil, want non-nil for a page beyond the document's page count")
	}
}
