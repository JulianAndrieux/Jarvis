package triage

import "testing"

func TestScore_AllPagesUsable(t *testing.T) {
	pages := []PageText{
		{Page: 1, Text: "Facture n. 2026-0042 Fournisseur: Acme SARL Total: 123.45 EUR"},
		{Page: 2, Text: "Conditions generales de vente applicables a toute commande."},
	}

	got := Score(pages, DefaultThresholds())

	if !got.HasTextLayer {
		t.Fatalf("HasTextLayer = false, want true (reasons: %v)", got.Reasons)
	}
	if got.Score != 1.0 {
		t.Errorf("Score = %v, want 1.0", got.Score)
	}
	if len(got.Pages) != 2 {
		t.Fatalf("len(Pages) = %d, want 2", len(got.Pages))
	}
	for _, p := range got.Pages {
		if !p.Usable {
			t.Errorf("page %d: Usable = false, want true", p.Page)
		}
	}
}

func TestScore_AllPagesEmpty(t *testing.T) {
	pages := []PageText{
		{Page: 1, Text: ""},
		{Page: 2, Text: "   \n  "},
	}

	got := Score(pages, DefaultThresholds())

	if got.HasTextLayer {
		t.Fatalf("HasTextLayer = true, want false (empty pages should never pass)")
	}
	if got.Score != 0.0 {
		t.Errorf("Score = %v, want 0.0", got.Score)
	}
	for _, p := range got.Pages {
		if p.Usable {
			t.Errorf("page %d: Usable = true, want false", p.Page)
		}
	}
}

func TestScore_MixedPagesBelowThreshold(t *testing.T) {
	// 1 page usable sur 2 => score 0.5, sous le seuil document par défaut (0.8).
	pages := []PageText{
		{Page: 1, Text: "Correspondance. Cher client, ceci est un courrier de test."},
		{Page: 2, Text: ""},
	}

	got := Score(pages, DefaultThresholds())

	if got.Score != 0.5 {
		t.Errorf("Score = %v, want 0.5", got.Score)
	}
	if got.HasTextLayer {
		t.Fatalf("HasTextLayer = true, want false (score 0.5 < threshold %v)", DefaultThresholds().DocumentThreshold)
	}
	if len(got.Reasons) == 0 {
		t.Error("Reasons is empty, want an explanation for the low score")
	}
}

func TestScore_TooFewCharsIsNotUsable(t *testing.T) {
	th := DefaultThresholds()
	pages := []PageText{
		// Sous MinCharsPerPage : un artefact OCR/watermark résiduel, pas une
		// vraie couche texte exploitable.
		{Page: 1, Text: "X"},
	}

	got := Score(pages, th)

	if got.Pages[0].Usable {
		t.Errorf("page with %d chars (< MinCharsPerPage=%d) marked usable", got.Pages[0].CharCount, th.MinCharsPerPage)
	}
}

func TestScore_GarbageTextIsNotUsable(t *testing.T) {
	// Suffisamment de caractères, mais essentiellement des symboles/bruit :
	// ne doit pas être considéré comme une couche texte exploitable.
	pages := []PageText{
		{Page: 1, Text: "#$%&/()=?*+~^^^###@@@!!!$$$%%%&&&///(((...---___***+++"},
	}

	got := Score(pages, DefaultThresholds())

	if got.Pages[0].Usable {
		t.Errorf("garbage page marked usable: %+v", got.Pages[0])
	}
}

func TestScore_NoPages(t *testing.T) {
	got := Score(nil, DefaultThresholds())

	if got.HasTextLayer {
		t.Error("HasTextLayer = true for a document with no pages, want false")
	}
	if got.Score != 0.0 {
		t.Errorf("Score = %v, want 0.0 for a document with no pages", got.Score)
	}
	if len(got.Reasons) == 0 {
		t.Error("Reasons is empty, want an explanation for a document with no pages")
	}
}

func TestScore_ExactlyAtDocumentThreshold(t *testing.T) {
	th := DefaultThresholds()
	th.DocumentThreshold = 0.5

	pages := []PageText{
		{Page: 1, Text: "Correspondance. Cher client, ceci est un courrier de test."},
		{Page: 2, Text: ""},
	}

	got := Score(pages, th)

	if got.Score != 0.5 {
		t.Fatalf("Score = %v, want 0.5", got.Score)
	}
	if !got.HasTextLayer {
		t.Error("HasTextLayer = false, want true (score 0.5 meets threshold 0.5)")
	}
}
