package testmap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestDiscover_FindsKnownTestsInRealModule(t *testing.T) {
	cats, err := Discover(moduleRoot(t))
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}

	var triage *Category
	for i := range cats {
		if cats[i].Package == "github.com/JulianAndrieux/Jarvis/internal/triage" {
			triage = &cats[i]
		}
	}
	if triage == nil {
		t.Fatal("internal/triage category not found")
	}

	names := map[string]TestFunc{}
	for _, tf := range triage.Tests {
		names[tf.Name] = tf
	}

	unit, ok := names["TestScore_AllPagesUsable"]
	if !ok {
		t.Fatal("TestScore_AllPagesUsable not found (unit test in score_test.go)")
	}
	if unit.Integration {
		t.Errorf("TestScore_AllPagesUsable.Integration = true, want false (no build tag)")
	}

	integ, ok := names["TestPdftotextExtractor_NativePDF"]
	if !ok {
		t.Fatal("TestPdftotextExtractor_NativePDF not found (integration test)")
	}
	if !integ.Integration {
		t.Errorf("TestPdftotextExtractor_NativePDF.Integration = false, want true (//go:build integration)")
	}
}

func TestDiscover_CategoriesSortedByPackage(t *testing.T) {
	cats, err := Discover(moduleRoot(t))
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	for i := 1; i < len(cats); i++ {
		if cats[i-1].Package >= cats[i].Package {
			t.Fatalf("categories not sorted: %q >= %q", cats[i-1].Package, cats[i].Package)
		}
	}
}

func TestDiscover_TestsSortedByName(t *testing.T) {
	cats, err := Discover(moduleRoot(t))
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	for _, cat := range cats {
		for i := 1; i < len(cat.Tests); i++ {
			if cat.Tests[i-1].Name >= cat.Tests[i].Name {
				t.Fatalf("%s: tests not sorted: %q >= %q", cat.Package, cat.Tests[i-1].Name, cat.Tests[i].Name)
			}
		}
	}
}

func TestDiscover_SyntheticModule_MixOfUnitAndIntegration(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.25\n")
	mustWriteFile(t, filepath.Join(dir, "pkg", "unit_test.go"), `package pkg

import "testing"

func TestSomething(t *testing.T) {}

func helperNotATest(t *testing.T) {} // pas exporté -> pas un test

func TestWrongSignature(x int) {} // pas *testing.T -> pas un test
`)
	mustWriteFile(t, filepath.Join(dir, "pkg", "integration_test.go"), `//go:build integration

package pkg

import "testing"

func TestRemote(t *testing.T) {}
`)

	cats, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	if len(cats) != 1 {
		t.Fatalf("len(cats) = %d, want 1", len(cats))
	}
	if cats[0].Package != "example.com/fixture/pkg" {
		t.Errorf("Package = %q, want example.com/fixture/pkg", cats[0].Package)
	}
	if len(cats[0].Tests) != 2 {
		t.Fatalf("len(Tests) = %d, want 2: %+v", len(cats[0].Tests), cats[0].Tests)
	}

	byName := map[string]TestFunc{}
	for _, tf := range cats[0].Tests {
		byName[tf.Name] = tf
	}
	if _, ok := byName["helperNotATest"]; ok {
		t.Error("helperNotATest was picked up, want it excluded (unexported, not Test-prefixed)")
	}
	if tf, ok := byName["TestSomething"]; !ok || tf.Integration {
		t.Errorf("TestSomething = %+v, want present and Integration=false", tf)
	}
	if tf, ok := byName["TestRemote"]; !ok || !tf.Integration {
		t.Errorf("TestRemote = %+v, want present and Integration=true", tf)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Jalon 24 : chaque test porte son code source (commentaire de doc
// compris) et les noms des packages importés par son fichier — pour
// l'afficher coloré dans la page Tests.
func TestDiscover_TestSourceAndFileImports(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.25\n")
	mustWriteFile(t, filepath.Join(dir, "pkg", "unit_test.go"), `package pkg

import (
	"testing"

	js "encoding/json"
)

// TestSomething vérifie quelque chose.
func TestSomething(t *testing.T) {
	_ = js.Valid(nil)
}
`)

	cats, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	tf := cats[0].Tests[0]
	wantSource := "// TestSomething vérifie quelque chose.\nfunc TestSomething(t *testing.T) {\n\t_ = js.Valid(nil)\n}"
	if tf.Source != wantSource {
		t.Errorf("Source = %q, want %q", tf.Source, wantSource)
	}
	if tf.Line != 10 {
		t.Errorf("Line = %d, want 10 (the func line, unchanged meaning)", tf.Line)
	}
	if strings.Join(tf.Imports, ",") != "js,testing" {
		t.Errorf("Imports = %v, want [js testing] (alias kept, sorted)", tf.Imports)
	}
}
