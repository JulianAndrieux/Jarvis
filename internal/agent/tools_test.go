package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                    "module example.com/app\n",
		"internal/store/store.go":   "package store\n\n// ListQuery filtre une liste.\ntype ListQuery struct {\n\tSearch string\n\tLimit  int\n}\n",
		"internal/store/fake.go":    "package store\n\nfunc (f *Fake) List(q ListQuery) {}\n",
		"cmd/app/main.go":           "package main\n\nfunc main() {}\n",
		".git/config":               "[core]\n",
		"bin/app":                   "\x00binaire",
		"data/results/x.json":       "{}",
		"internal/store/secret.txt": strings.Repeat("ligne\n", 500),
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	return root
}

func TestTools_ReadFile_NumberedAndRanged(t *testing.T) {
	tools := Tools{Root: writeRepo(t)}
	out := tools.ReadFile("internal/store/store.go", 3, 5)
	want := "3\t// ListQuery filtre une liste.\n4\ttype ListQuery struct {\n5\t\tSearch string\n"
	if out != want {
		t.Errorf("ReadFile(3-5) = %q, want %q", out, want)
	}
}

func TestTools_ReadFile_TruncatesLongFiles(t *testing.T) {
	tools := Tools{Root: writeRepo(t), MaxLines: 100}
	out := tools.ReadFile("internal/store/secret.txt", 0, 0)
	if strings.Count(out, "\n") > 101 || !strings.Contains(out, "tronqué") {
		t.Errorf("ReadFile should stop at MaxLines and say so: %d lines", strings.Count(out, "\n"))
	}
}

// L'agent ne sort jamais du dépôt, et ne lit ni l'historique git ni les
// dossiers de binaires/données.
func TestTools_ConfinedToRepository(t *testing.T) {
	root := writeRepo(t)
	outside := filepath.Join(t.TempDir(), "hors-depot.txt")
	os.WriteFile(outside, []byte("secret"), 0o644)
	os.Symlink(outside, filepath.Join(root, "lien"))
	tools := Tools{Root: root}

	for _, path := range []string{"../hors-depot.txt", "/etc/passwd", ".git/config", "lien", "internal/../../x", "bin/app", "data/results/x.json"} {
		if out := tools.ReadFile(path, 0, 0); !strings.HasPrefix(out, "ERREUR") {
			t.Errorf("ReadFile(%q) = %q, want an ERREUR (outside the allowed repository)", path, out)
		}
	}
}

func TestTools_ListFiles(t *testing.T) {
	tools := Tools{Root: writeRepo(t)}
	out := tools.ListFiles("")
	for _, want := range []string{"cmd/", "internal/", "go.mod"} {
		if !strings.Contains(out, want) {
			t.Errorf("ListFiles(root) = %q, want %q", out, want)
		}
	}
	for _, hidden := range []string{".git", "bin/", "data/"} {
		if strings.Contains(out, hidden) {
			t.Errorf("ListFiles(root) lists %q: %q", hidden, out)
		}
	}
	if sub := tools.ListFiles("internal/store"); !strings.Contains(sub, "store.go") {
		t.Errorf("ListFiles(internal/store) = %q", sub)
	}
}

func TestTools_Search(t *testing.T) {
	tools := Tools{Root: writeRepo(t)}
	out := tools.Search(`ListQuery`, "")
	for _, want := range []string{"internal/store/store.go:3:", "internal/store/fake.go:3:"} {
		if !strings.Contains(out, want) {
			t.Errorf("Search(ListQuery) = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, ".git") {
		t.Errorf("Search must skip .git: %q", out)
	}
	if out := tools.Search(`(`, ""); !strings.HasPrefix(out, "ERREUR") {
		t.Errorf("invalid regexp: %q, want an ERREUR, not a panic", out)
	}
}

func TestTools_Execute_DispatchesJSONArguments(t *testing.T) {
	tools := Tools{Root: writeRepo(t)}
	out := tools.Execute(ToolCall{Name: "read_file", Arguments: `{"path": "cmd/app/main.go", "start_line": 3, "end_line": 3}`})
	if out != "3\tfunc main() {}\n" {
		t.Errorf("Execute(read_file) = %q", out)
	}
	if out := tools.Execute(ToolCall{Name: "rm_rf", Arguments: `{}`}); !strings.HasPrefix(out, "ERREUR") {
		t.Errorf("unknown tool: %q", out)
	}
	if out := tools.Execute(ToolCall{Name: "read_file", Arguments: `pas du json`}); !strings.HasPrefix(out, "ERREUR") {
		t.Errorf("invalid arguments: %q", out)
	}
}

// Trouvé en conditions réelles (Qwen3-8B, jalon 27) : l'agent a demandé
// 8 fois "templates/documents.html" (le vrai fichier est documents.templ)
// en recevant une erreur brute. L'outil suggère désormais les fichiers
// proches — un modèle modeste rebondit sur une piste, pas sur une erreur.
func TestTools_ReadFile_MissingFileSuggestsNeighbours(t *testing.T) {
	root := writeRepo(t)
	os.WriteFile(filepath.Join(root, "internal/store/documents.templ"), []byte("templ X() {}\n"), 0o644)
	out := Tools{Root: root}.ReadFile("internal/store/documents.html", 0, 0)
	if !strings.HasPrefix(out, "ERREUR") || !strings.Contains(out, "internal/store/documents.templ") {
		t.Errorf("ReadFile(missing) = %q, want an error suggesting documents.templ", out)
	}
}

// Lire un dossier : dire que c'en est un et montrer son contenu, au lieu
// de "(aucune ligne dans l'intervalle ; le fichier en compte au moins 0)".
func TestTools_ReadFile_OnADirectoryListsIt(t *testing.T) {
	out := Tools{Root: writeRepo(t)}.ReadFile("internal/store", 0, 0)
	if !strings.Contains(out, "dossier") || !strings.Contains(out, "store.go") {
		t.Errorf("ReadFile(dir) = %q, want it to say it is a directory and list it", out)
	}
}

// Vu en réel (ticket "Ajouter commentaire sur document") : la recherche
// remontait surtout CLAUDE.md, qui noyait le code — le contexte du
// projet est déjà donné à part. Les fichiers Markdown sont exclus.
func TestTools_SearchSkipsMarkdownDocs(t *testing.T) {
	root := writeRepo(t)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("ListQuery est décrit ici\n"), 0o644)
	out := Tools{Root: root}.Search("ListQuery", "")
	if strings.Contains(out, "CLAUDE.md") {
		t.Errorf("search returned Markdown docs: %q", out)
	}
	if !strings.Contains(out, "internal/store/store.go") {
		t.Errorf("search lost the code hit: %q", out)
	}
}

func TestTools_ListFilesOnAFileSaysSo(t *testing.T) {
	out := Tools{Root: writeRepo(t)}.ListFiles("internal/store/store.go")
	if !strings.Contains(out, "est un fichier") || !strings.Contains(out, "read_file") {
		t.Errorf("ListFiles(file) = %q", out)
	}
}
