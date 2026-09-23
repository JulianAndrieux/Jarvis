package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeChecker struct {
	checks, tests string
	ok            bool
	gotPkg        string
}

func (f *fakeChecker) Checks(ctx context.Context, dir string) (string, bool) { return f.checks, f.ok }
func (f *fakeChecker) Tests(ctx context.Context, dir, pkg string) (string, bool) {
	f.gotPkg = pkg
	return f.tests, f.ok
}

func devTools(t *testing.T) (DevTools, string) {
	root := writeRepo(t)
	return DevTools{Tools: Tools{Root: root}, Checker: &fakeChecker{checks: "gofmt : ok", tests: "ok", ok: true}}, root
}

func TestDevTools_WriteFileCreatesDirsAndIsConfined(t *testing.T) {
	d, root := devTools(t)
	out := d.Execute(context.Background(), ToolCall{Name: "write_file", Arguments: `{"path": "internal/pages/pages.go", "content": "package pages\n"}`})
	if strings.HasPrefix(out, "ERREUR") {
		t.Fatalf("write_file = %q", out)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "internal/pages/pages.go")); string(got) != "package pages\n" {
		t.Errorf("file content = %q", got)
	}
	for _, path := range []string{"../evasion.go", "/tmp/x.go", ".git/hooks/pre-commit", "go.mod", "go.sum", "cmd/app/page_templ.go", "CLAUDE.md", "bin/app"} {
		if out := d.Execute(context.Background(), ToolCall{Name: "write_file", Arguments: `{"path": "` + path + `", "content": "x"}`}); !strings.HasPrefix(out, "ERREUR") {
			t.Errorf("write_file(%s) = %q, want a refusal", path, out)
		}
	}
}

func TestDevTools_EditFileExactUniqueReplacement(t *testing.T) {
	d, root := devTools(t)
	out := d.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: `{"path": "internal/store/store.go", "old": "\tLimit  int\n", "new": "\tLimit  int\n\tSince  string\n"}`})
	if strings.HasPrefix(out, "ERREUR") {
		t.Fatalf("edit_file = %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "internal/store/store.go"))
	if !strings.Contains(string(got), "\tSince  string\n") {
		t.Errorf("file after edit:\n%s", got)
	}
}

// Un petit modèle recopie souvent un extrait avec une indentation
// approximative : l'outil lui montre les lignes réelles les plus proches.
func TestDevTools_EditFileNotFoundShowsNearbyLines(t *testing.T) {
	d, _ := devTools(t)
	out := d.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: `{"path": "internal/store/store.go", "old": "Search string\nLimit int", "new": "x"}`})
	if !strings.HasPrefix(out, "ERREUR") || !strings.Contains(out, "5\t\tSearch string") {
		t.Errorf("edit_file(not found) = %q, want an error showing line 5", out)
	}
}

func TestDevTools_EditFileAmbiguousIsRefused(t *testing.T) {
	d, root := devTools(t)
	os.WriteFile(filepath.Join(root, "dup.go"), []byte("x := 1\nx := 1\n"), 0o644)
	out := d.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: `{"path": "dup.go", "old": "x := 1", "new": "y"}`})
	if !strings.HasPrefix(out, "ERREUR") || !strings.Contains(out, "2") {
		t.Errorf("edit_file(ambiguous) = %q, want a refusal naming the 2 occurrences", out)
	}
}

func TestDevTools_ChecksAndTestsDelegateToChecker(t *testing.T) {
	d, _ := devTools(t)
	if out := d.Execute(context.Background(), ToolCall{Name: "run_checks", Arguments: `{}`}); !strings.Contains(out, "gofmt : ok") {
		t.Errorf("run_checks = %q", out)
	}
	d.Execute(context.Background(), ToolCall{Name: "run_tests", Arguments: `{"package": "./internal/store/"}`})
	if got := d.Checker.(*fakeChecker).gotPkg; got != "./internal/store/" {
		t.Errorf("run_tests package = %q", got)
	}
	// Un paquet hors du dépôt n'est pas accepté comme motif de test.
	if out := d.Execute(context.Background(), ToolCall{Name: "run_tests", Arguments: `{"package": "../autre/..."}`}); !strings.HasPrefix(out, "ERREUR") {
		t.Errorf("run_tests(../) = %q, want a refusal", out)
	}
}

func TestDevSpecs_OfferWriteToolsAndFinish(t *testing.T) {
	names := map[string]bool{}
	for _, s := range DevSpecs() {
		names[s.Name] = true
	}
	for _, want := range []string{"list_files", "read_file", "search", "write_file", "edit_file", "run_checks", "run_tests", "finish"} {
		if !names[want] {
			t.Errorf("DevSpecs lacks %s", want)
		}
	}
	if names["propose_plan"] {
		t.Error("propose_plan belongs to the analysis phase")
	}
}

func TestValidPackagePattern(t *testing.T) {
	for _, ok := range []string{"./...", "./internal/webapp/", "./internal/...", "./cmd/jarvisapp"} {
		if !validPackagePattern(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"../autre/...", "./../x", "./a/../../b", "/abs", "github.com/x/y", "./x/..../"} {
		if validPackagePattern(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Vu en réel (jalon 28, Qwen3-8B) : pour ajouter une ligne, l'agent a
// réécrit detect.go en entier (12 800 caractères) — réponse coupée par le
// contexte de 8192 jetons, JSON invalide, développement perdu. Réécrire
// un fichier existant de plus de 60 lignes est refusé : edit_file.
func TestDevTools_WriteFileRefusesRewritingALargeExistingFile(t *testing.T) {
	d, root := devTools(t)
	big := strings.Repeat("// ligne\n", 80)
	os.WriteFile(filepath.Join(root, "gros.go"), []byte("package app\n"+big), 0o644)
	out := d.Execute(context.Background(), ToolCall{Name: "write_file", Arguments: `{"path": "gros.go", "content": "package app\n"}`})
	if !strings.HasPrefix(out, "ERREUR") || !strings.Contains(out, "edit_file") {
		t.Errorf("write_file(existing 81-line file) = %q, want a refusal pointing to edit_file", out)
	}
	// Un petit fichier existant peut être réécrit.
	os.WriteFile(filepath.Join(root, "petit.go"), []byte("package app\n"), 0o644)
	if out := d.Execute(context.Background(), ToolCall{Name: "write_file", Arguments: `{"path": "petit.go", "content": "package app\n\nfunc A() {}\n"}`}); strings.HasPrefix(out, "ERREUR") {
		t.Errorf("write_file(small file) = %q", out)
	}
}
