package codemap

import (
	"os"
	"path/filepath"
	"strings"
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

// --- Jalon 24 : types lisibles, code source, références entre types ---

const pipelinePkg = "github.com/JulianAndrieux/Jarvis/internal/pipeline"

func analyzePipelineAndWebapp(t *testing.T) *Model {
	t.Helper()
	m, err := Analyze(moduleRoot(t), "./internal/pipeline", "./internal/webapp", "./internal/extraction")
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return m
}

func field(t *testing.T, ti *TypeInfo, name string) FieldInfo {
	t.Helper()
	for _, f := range ti.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %s.%s not found", ti.Name, name)
	return FieldInfo{}
}

func TestAnalyze_FieldTypesAreShortNotFullImportPaths(t *testing.T) {
	m := analyzePipelineAndWebapp(t)
	res, _ := m.Type(pipelinePkg, "Result")

	if got := field(t, res, "Extraction").Type; got != "[]extraction.Result" {
		t.Errorf("Result.Extraction type = %q, want []extraction.Result", got)
	}
	if got := field(t, res, "Pages").Type; got != "[]PageContent" {
		t.Errorf("Result.Pages type = %q, want []PageContent (same package: unqualified)", got)
	}
}

func TestAnalyze_MethodSignaturesAreShort(t *testing.T) {
	m := analyzePipelineAndWebapp(t)
	p, _ := m.Type(pipelinePkg, "Pipeline")
	for _, meth := range p.Methods {
		if meth.Name == "RunAuto" {
			if strings.Contains(meth.Signature, "github.com/") {
				t.Errorf("RunAuto signature = %q, want no full import paths", meth.Signature)
			}
			if !strings.Contains(meth.Signature, "context.Context") {
				t.Errorf("RunAuto signature = %q, want context.Context", meth.Signature)
			}
			return
		}
	}
	t.Fatal("method RunAuto not found")
}

func TestAnalyze_TypeAndMethodSourceWithLocation(t *testing.T) {
	m := analyzePipelineAndWebapp(t)
	res, _ := m.Type(pipelinePkg, "Result")

	if !strings.Contains(res.Source, "type Result struct {") || !strings.Contains(res.Source, "SearchText string") {
		t.Errorf("Result.Source = %q, want the full type declaration", res.Source)
	}
	if !strings.HasPrefix(res.Source, "//") {
		t.Errorf("Result.Source should start with its doc comment, got %q", firstLine(res.Source))
	}
	if res.File != "internal/pipeline/pipeline.go" || res.Line <= 0 {
		t.Errorf("Result location = %s:%d, want internal/pipeline/pipeline.go:>0", res.File, res.Line)
	}

	p, _ := m.Type(pipelinePkg, "Pipeline")
	for _, meth := range p.Methods {
		if meth.Name == "RunAuto" {
			if !strings.Contains(meth.Source, "func (p Pipeline) RunAuto(") || meth.File == "" || meth.Line <= 0 {
				t.Errorf("RunAuto source/location = %q at %s:%d", firstLine(meth.Source), meth.File, meth.Line)
			}
			return
		}
	}
	t.Fatal("method RunAuto not found")
}

func TestAnalyze_FieldReferencesToModuleTypesWithCardinality(t *testing.T) {
	m := analyzePipelineAndWebapp(t)

	res, _ := m.Type(pipelinePkg, "Result")
	ext := field(t, res, "Extraction")
	if len(ext.Refs) != 1 || ext.Refs[0] != (TypeRef{Package: "github.com/JulianAndrieux/Jarvis/internal/extraction", Name: "Result"}) || !ext.Many {
		t.Errorf("Result.Extraction refs = %+v many=%v, want extraction.Result, many", ext.Refs, ext.Many)
	}
	if search := field(t, res, "SearchText"); len(search.Refs) != 0 {
		t.Errorf("Result.SearchText refs = %+v, want none (string)", search.Refs)
	}

	job, _ := m.Type("github.com/JulianAndrieux/Jarvis/internal/webapp", "Job")
	r := field(t, job, "Result")
	if len(r.Refs) != 1 || r.Refs[0].Name != "Result" || !r.Optional || r.Many {
		t.Errorf("Job.Result refs = %+v optional=%v many=%v, want pipeline.Result, optional", r.Refs, r.Optional, r.Many)
	}
}

func TestAnalyze_ModuleAndRelativePackagePaths(t *testing.T) {
	m := analyzePipelineAndWebapp(t)
	if m.ModulePath != "github.com/JulianAndrieux/Jarvis" {
		t.Errorf("ModulePath = %q", m.ModulePath)
	}
	if got := m.ShortPath(pipelinePkg); got != "internal/pipeline" {
		t.Errorf("ShortPath(pipeline) = %q, want internal/pipeline", got)
	}
	if got := m.ShortPath("context"); got != "context" {
		t.Errorf("ShortPath(context) = %q, want unchanged for packages outside the module", got)
	}
	p, _ := m.Package(pipelinePkg)
	found := false
	for _, n := range p.ImportNames {
		if n == "extraction" {
			found = true
		}
	}
	if !found {
		t.Errorf("ImportNames = %v, want it to include extraction", p.ImportNames)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Les méthodes d'une interface sont portées par son type sous-jacent, pas
// par le type nommé : elles n'étaient pas listées (fiche UML et diagramme
// vides pour toute interface).
func TestAnalyze_InterfaceMethodsAreListed(t *testing.T) {
	m, err := Analyze(moduleRoot(t), "./internal/vlm")
	if err != nil {
		t.Fatal(err)
	}
	client, ok := m.Type("github.com/JulianAndrieux/Jarvis/internal/vlm", "Client")
	if !ok {
		t.Fatal("vlm.Client not found")
	}
	if len(client.Methods) != 1 || client.Methods[0].Name != "ParsePage" {
		t.Fatalf("Client methods = %+v, want [ParsePage]", client.Methods)
	}
	if sig := client.Methods[0].Signature; !strings.Contains(sig, "PageImage") || strings.Contains(sig, "github.com/") {
		t.Errorf("ParsePage signature = %q, want short types", sig)
	}
}
