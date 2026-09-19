package bbox

import "testing"

func words(specs ...string) []Word {
	// Chaque spec est "texte:xMin,yMin,xMax,yMax" pour rester lisible dans
	// les tests. Les coordonnées sont arbitraires mais croissantes en x
	// pour simuler des mots alignés sur une ligne.
	out := make([]Word, len(specs))
	for i, s := range specs {
		out[i] = Word{Text: s, XMin: float64(i) * 10, YMin: 0, XMax: float64(i)*10 + 8, YMax: 10}
	}
	return out
}

func TestFindSnippetBBox_ExactContiguousMatch(t *testing.T) {
	ws := words("Facture", "n.", "2026-0042", "Fournisseur:", "Acme", "SARL")

	got, ok := FindSnippetBBox(ws, "Facture n. 2026-0042")
	if !ok {
		t.Fatal("FindSnippetBBox() ok = false, want true")
	}
	want := Box{XMin: ws[0].XMin, YMin: ws[0].YMin, XMax: ws[2].XMax, YMax: ws[2].YMax}
	if got != want {
		t.Errorf("FindSnippetBBox() = %+v, want %+v", got, want)
	}
}

func TestFindSnippetBBox_SingleWord(t *testing.T) {
	ws := words("Total:", "123.45", "EUR")

	got, ok := FindSnippetBBox(ws, "123.45")
	if !ok {
		t.Fatal("FindSnippetBBox() ok = false, want true")
	}
	want := Box{XMin: ws[1].XMin, YMin: ws[1].YMin, XMax: ws[1].XMax, YMax: ws[1].YMax}
	if got != want {
		t.Errorf("FindSnippetBBox() = %+v, want %+v", got, want)
	}
}

func TestFindSnippetBBox_CaseInsensitive(t *testing.T) {
	ws := words("Acme", "SARL")

	got, ok := FindSnippetBBox(ws, "acme sarl")
	if !ok {
		t.Fatal("FindSnippetBBox() ok = false, want true")
	}
	want := Box{XMin: ws[0].XMin, YMin: ws[0].YMin, XMax: ws[1].XMax, YMax: ws[1].YMax}
	if got != want {
		t.Errorf("FindSnippetBBox() = %+v, want %+v", got, want)
	}
}

func TestFindSnippetBBox_TrailingPunctuationIgnored(t *testing.T) {
	ws := words("Fournisseur:", "Acme", "SARL")

	// Le snippet peut avoir été recopié sans le ":" final du mot source.
	got, ok := FindSnippetBBox(ws, "Fournisseur")
	if !ok {
		t.Fatal("FindSnippetBBox() ok = false, want true")
	}
	want := Box{XMin: ws[0].XMin, YMin: ws[0].YMin, XMax: ws[0].XMax, YMax: ws[0].YMax}
	if got != want {
		t.Errorf("FindSnippetBBox() = %+v, want %+v", got, want)
	}
}

func TestFindSnippetBBox_NotFound_FallsBackToEnvelope(t *testing.T) {
	ws := words("Facture", "n.", "2026-0042", "Fournisseur:", "Acme", "SARL")

	// "Facture 2026-0042" n'est pas une sous-séquence contiguë (le mot
	// "n." est entre les deux) : repli sur l'enveloppe des tokens trouvés
	// individuellement, dans l'ordre.
	got, ok := FindSnippetBBox(ws, "Facture 2026-0042")
	if !ok {
		t.Fatal("FindSnippetBBox() ok = false, want true (fallback envelope)")
	}
	want := Box{XMin: ws[0].XMin, YMin: ws[0].YMin, XMax: ws[2].XMax, YMax: ws[2].YMax}
	if got != want {
		t.Errorf("FindSnippetBBox() = %+v, want %+v", got, want)
	}
}

func TestFindSnippetBBox_NoTokenFound_ReturnsFalse(t *testing.T) {
	ws := words("Facture", "n.", "2026-0042")

	_, ok := FindSnippetBBox(ws, "totalement absent")
	if ok {
		t.Error("FindSnippetBBox() ok = true, want false when nothing matches")
	}
}

func TestFindSnippetBBox_EmptySnippet_ReturnsFalse(t *testing.T) {
	ws := words("Facture")

	_, ok := FindSnippetBBox(ws, "")
	if ok {
		t.Error("FindSnippetBBox() ok = true, want false for an empty snippet")
	}
}

func TestFindSnippetBBox_EmptyWords_ReturnsFalse(t *testing.T) {
	_, ok := FindSnippetBBox(nil, "Facture")
	if ok {
		t.Error("FindSnippetBBox() ok = true, want false when there are no words on the page")
	}
}
