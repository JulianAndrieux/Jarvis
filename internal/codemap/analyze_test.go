package codemap

import (
	"os"
	"path/filepath"
	"testing"
)

// moduleRoot remonte depuis le répertoire courant jusqu'à trouver go.mod
// — évite de coder en dur la profondeur de internal/codemap sous la
// racine du module.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found in any parent directory")
		}
		dir = parent
	}
}

// Ces tests s'appuient volontairement sur le module Jarvis lui-même
// (auto-référentiel) : c'est à la fois le meilleur fixture disponible
// (une vraie architecture avec de vrais embeddings et interfaces) et une
// vérification vivante que l'analyseur voit correctement les faits
// qu'on connaît déjà par construction (ex. jalon 9 : DocumentRecord
// embed RecordMeta).

func TestAnalyze_FindsStructWithFields(t *testing.T) {
	m, err := Analyze(moduleRoot(t), "./internal/schema")
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil", err)
	}

	typ, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/schema", "Field")
	if !ok {
		t.Fatal(`Type("internal/schema", "Field") not found`)
	}
	if typ.Kind != KindStruct {
		t.Errorf("Kind = %q, want struct", typ.Kind)
	}
	var names []string
	for _, f := range typ.Fields {
		names = append(names, f.Name)
	}
	for _, want := range []string{"Value", "Confidence", "SourceSnippet"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Fields = %v, want it to contain %q", names, want)
		}
	}
}

func TestAnalyze_DetectsEmbedding(t *testing.T) {
	m, err := Analyze(moduleRoot(t), "./internal/store")
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil", err)
	}

	doc, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/store", "DocumentRecord")
	if !ok {
		t.Fatal(`Type("internal/store", "DocumentRecord") not found`)
	}

	wantEmbed := TypeRef{Package: "github.com/JulianAndrieux/Jarvis/internal/store", Name: "RecordMeta"}
	found := false
	for _, e := range doc.Embeds {
		if e == wantEmbed {
			found = true
		}
	}
	if !found {
		t.Errorf("DocumentRecord.Embeds = %v, want it to contain %v", doc.Embeds, wantEmbed)
	}

	// Vérifie aussi l'index inversé : RecordMeta doit lister DocumentRecord
	// (et PageRecord) dans EmbeddedBy.
	meta, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/store", "RecordMeta")
	if !ok {
		t.Fatal(`Type("internal/store", "RecordMeta") not found`)
	}
	wantEmbeddedBy := TypeRef{Package: "github.com/JulianAndrieux/Jarvis/internal/store", Name: "DocumentRecord"}
	found = false
	for _, e := range meta.EmbeddedBy {
		if e == wantEmbeddedBy {
			found = true
		}
	}
	if !found {
		t.Errorf("RecordMeta.EmbeddedBy = %v, want it to contain %v", meta.EmbeddedBy, wantEmbeddedBy)
	}
}

func TestAnalyze_DetectsInterfaceImplementations(t *testing.T) {
	m, err := Analyze(moduleRoot(t), "./internal/triage")
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil", err)
	}

	iface, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/triage", "TextExtractor")
	if !ok {
		t.Fatal(`Type("internal/triage", "TextExtractor") not found`)
	}
	if iface.Kind != KindInterface {
		t.Errorf("Kind = %q, want interface", iface.Kind)
	}

	wantImpls := map[string]bool{"PdftotextExtractor": false, "FakeExtractor": false}
	for _, impl := range iface.ImplementedBy {
		if _, ok := wantImpls[impl.Name]; ok {
			wantImpls[impl.Name] = true
		}
	}
	for name, found := range wantImpls {
		if !found {
			t.Errorf("TextExtractor.ImplementedBy = %v, want it to contain %q", iface.ImplementedBy, name)
		}
	}

	// Et dans l'autre sens : PdftotextExtractor doit lister TextExtractor
	// dans Implements.
	impl, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/triage", "PdftotextExtractor")
	if !ok {
		t.Fatal(`Type("internal/triage", "PdftotextExtractor") not found`)
	}
	wantIface := TypeRef{Package: "github.com/JulianAndrieux/Jarvis/internal/triage", Name: "TextExtractor"}
	found := false
	for _, i := range impl.Implements {
		if i == wantIface {
			found = true
		}
	}
	if !found {
		t.Errorf("PdftotextExtractor.Implements = %v, want it to contain %v", impl.Implements, wantIface)
	}
}

func TestAnalyze_FindsDeclaredMethods(t *testing.T) {
	m, err := Analyze(moduleRoot(t), "./internal/triage")
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil", err)
	}

	typ, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/triage", "PdftotextExtractor")
	if !ok {
		t.Fatal(`Type("internal/triage", "PdftotextExtractor") not found`)
	}

	found := false
	for _, meth := range typ.Methods {
		if meth.Name == "ExtractPerPage" {
			found = true
		}
	}
	if !found {
		t.Errorf("PdftotextExtractor.Methods = %+v, want it to contain ExtractPerPage", typ.Methods)
	}
}

func TestAnalyze_UnknownPackage_NotFound(t *testing.T) {
	m, err := Analyze(moduleRoot(t), "./internal/triage")
	if err != nil {
		t.Fatalf("Analyze() error = %v, want nil", err)
	}
	_, ok := m.Package("does/not/exist")
	if ok {
		t.Error(`Package("does/not/exist") ok = true, want false`)
	}
}
