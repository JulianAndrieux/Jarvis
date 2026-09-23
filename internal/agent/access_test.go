package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Vu en réel (ticket "Déplacer le filtre date documents") : macOS a
// retiré à Jarvis l'accès au dossier Documents, où vit le dépôt. La
// recherche répondait « (aucun résultat) » (dossier illisible ignoré),
// le modèle a cherché douze fois des mots présents partout, et l'échec
// accusait le ticket (« trop gros ou plan à préciser »).

// lockedDir : un dossier illisible (même classe d'erreur que le refus de
// macOS : fs.ErrPermission).
func lockedDir(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root lit tout : impossible de simuler un refus d'accès")
	}
	os.MkdirAll(path, 0o755)
	os.WriteFile(filepath.Join(path, "x.go"), []byte("package x // documents\n"), 0o644)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o755) })
}

func TestTools_AccessDeniedIsReportedNotHidden(t *testing.T) {
	root := writeRepo(t)
	lockedDir(t, filepath.Join(root, "internal", "webapp"))
	tools := Tools{Root: root}

	for name, out := range map[string]string{
		"list_files": tools.ListFiles("internal/webapp"),
		"search":     tools.Search("documents", "internal/webapp"),
	} {
		if !strings.HasPrefix(out, accessDeniedPrefix) {
			t.Errorf("%s = %q, want an access error", name, out)
		}
	}
	// Un sous-dossier illisible dans une recherche plus large : signalé.
	if out := tools.Search("ListQuery", "."); !strings.Contains(out, "internal/webapp") || !strings.Contains(out, "illisible") {
		t.Errorf("search . = %q, want the unreadable folder named", out)
	}
}

// Dépôt entièrement illisible : l'agent s'arrête avant d'appeler le
// modèle, avec une raison qui désigne l'accès, pas le ticket.
func TestAnalyze_UnreadableRepositoryStopsBeforeTheModel(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Jarvis")
	lockedDir(t, root)
	model := &scriptedModel{}
	a := &Analyzer{Model: model, Tools: Tools{Root: root}}
	_, err := a.Analyze(context.Background(), request(), nil)
	if err == nil || !strings.Contains(err.Error(), "n'a pas accès au dépôt") || strings.Contains(err.Error(), "ticket est peut-être trop gros") {
		t.Fatalf("err = %v, want an access error that does not blame the ticket", err)
	}
	if len(model.calls) != 0 {
		t.Errorf("model called %d times, want 0", len(model.calls))
	}
}

// Accès perdu en cours de route : arrêt dès la première erreur d'accès,
// sans laisser le modèle tourner en rond.
func TestLoop_AccessErrorStopsAtOnce(t *testing.T) {
	root := writeRepo(t)
	lockedDir(t, filepath.Join(root, "internal", "webapp"))
	model := &scriptedModel{replies: []Message{
		call("l", "list_files", `{"dir": "internal/webapp"}`),
		call("s", "search", `{"pattern": "x"}`),
	}}
	a := &Analyzer{Model: model, Tools: Tools{Root: root}}
	_, err := a.Analyze(context.Background(), request(), nil)
	if err == nil || !strings.Contains(err.Error(), "n'a pas accès au dépôt") {
		t.Fatalf("err = %v, want an access error", err)
	}
	if len(model.calls) != 1 {
		t.Errorf("model called %d times, want a stop after the first access error", len(model.calls))
	}
}
