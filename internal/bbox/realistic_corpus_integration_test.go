//go:build integration

package bbox

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPdftotextBBoxExtractor_MultiligneTable_WordsPositioned(t *testing.T) {
	// facture_multiligne.pdf a un tableau de lignes (plusieurs colonnes,
	// plusieurs montants proches) — un cas bien plus dense que les
	// fixtures d'origine, pour vérifier que l'extraction de mots
	// positionnés tient sur une mise en page réaliste.
	e := PdftotextBBoxExtractor{}
	path := filepath.Join("..", "..", "testdata", "fixtures", "facture_multiligne.pdf")

	pages, err := e.ExtractWords(context.Background(), path)
	if err != nil {
		t.Fatalf("ExtractWords() error = %v, want nil", err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	words := pages[0].Words
	if len(words) < 20 {
		t.Fatalf("len(words) = %d, want a dense page (>= 20 words)", len(words))
	}

	// Le "Total TTC" final doit être localisable, distinct du "Total HT"
	// et de la "TVA" qui apparaissent juste au-dessus.
	box, ok := FindSnippetBBox(words, "Total TTC: 215.64 EUR")
	if !ok {
		t.Fatal("FindSnippetBBox() ok = false, want the final total to be found")
	}
	htBox, htOK := FindSnippetBBox(words, "Total HT: 179.70 EUR")
	if !htOK {
		t.Fatal("FindSnippetBBox() ok = false for Total HT")
	}
	if box.YMin == htBox.YMin {
		t.Errorf("Total TTC and Total HT resolved to the same row (YMin=%v for both), want distinct rows", box.YMin)
	}
}

func TestPdftotextBBoxExtractor_ScannedRealiste_HasNoWords(t *testing.T) {
	// Cohérent avec le triage : une page image-only n'a pas de mots
	// positionnés à extraire, pivotée ou non.
	e := PdftotextBBoxExtractor{}
	path := filepath.Join("..", "..", "testdata", "fixtures", "facture_scannee_realiste.pdf")

	pages, err := e.ExtractWords(context.Background(), path)
	if err != nil {
		t.Fatalf("ExtractWords() error = %v, want nil", err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	if len(pages[0].Words) != 0 {
		t.Errorf("Words = %v, want empty for an image-only page", pages[0].Words)
	}
}
