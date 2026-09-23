package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", msg)
}

// Le ticket a travaillé pendant que main avançait : la copie intègre main,
// puis main avance jusqu'à elle sans commit de fusion côté main.
func TestManager_SyncThenPromote(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	dir, _ := m.Prepare(ctx, "t1")
	commitFile(t, dir, "ticket.go", "package a\n", "travail du ticket")
	commitFile(t, repo, "main.go", "package a\n", "main avance")

	if err := m.SyncWithBase(ctx, dir); err != nil {
		t.Fatalf("SyncWithBase() = %v", err)
	}
	prev, err := m.BaseHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	head, err := m.Promote(ctx, "t1")
	if err != nil {
		t.Fatalf("Promote() = %v", err)
	}
	if head == prev || head != gitIn(t, repo, "rev-parse", "HEAD") {
		t.Errorf("main = %s (avant %s), want advanced to the ticket branch", head, prev)
	}
	for _, f := range []string{"ticket.go", "main.go"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Errorf("%s absent of the main checkout after promotion", f)
		}
	}
}

func TestManager_SyncConflictIsAbortedAndReported(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	dir, _ := m.Prepare(ctx, "t1")
	commitFile(t, dir, "a.go", "package a // ticket\n", "ticket")
	commitFile(t, repo, "a.go", "package a // main\n", "main")

	err := m.SyncWithBase(ctx, dir)
	if err == nil || !strings.Contains(err.Error(), "a.go") {
		t.Fatalf("SyncWithBase() = %v, want a conflict error naming a.go", err)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("merge not aborted, workspace left dirty:\n%s", st)
	}
}

// main n'avance que par avance rapide, et jamais par-dessus du travail non
// commité du dépôt principal.
func TestManager_PromoteRefusals(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	dir, _ := m.Prepare(ctx, "t1")
	commitFile(t, dir, "ticket.go", "package a\n", "ticket")

	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a // en cours\n"), 0o644)
	if err := m.BaseClean(ctx); err == nil || !strings.Contains(err.Error(), "a.go") {
		t.Errorf("BaseClean() = %v, want an error naming the uncommitted file", err)
	}
	gitIn(t, repo, "checkout", "--", "a.go")
	os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("non suivi"), 0o644)
	if err := m.BaseClean(ctx); err != nil {
		t.Errorf("untracked files must not block: %v", err)
	}

	commitFile(t, repo, "main.go", "package a\n", "main avance sans que le ticket l'intègre")
	if _, err := m.Promote(ctx, "t1"); err == nil {
		t.Error("Promote() must refuse anything but a fast-forward")
	}
}

// Retour arrière : un commit qui rétablit l'arbre d'avant, sans réécrire
// l'historique.
func TestManager_RestoreBase(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	prev, _ := m.BaseHead(ctx)
	commitFile(t, repo, "b.go", "package a\n", "déployé")
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a // modifié\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "-am", "déployé 2")
	deployed, _ := m.BaseHead(ctx)

	head, err := m.RestoreBase(ctx, prev, "Retour arrière du ticket t1")
	if err != nil {
		t.Fatalf("RestoreBase() = %v", err)
	}
	if gitIn(t, repo, "rev-parse", head+"^{tree}") != gitIn(t, repo, "rev-parse", prev+"^{tree}") {
		t.Error("restored tree differs from the previous one")
	}
	if gitIn(t, repo, "rev-parse", head+"^") != deployed {
		t.Error("the restore must be a new commit on top of main, not a rewrite")
	}
	if _, err := os.Stat(filepath.Join(repo, "b.go")); err == nil {
		t.Error("b.go still in the checkout")
	}
	if !strings.Contains(gitIn(t, repo, "log", "-1", "--format=%s"), "Retour arrière du ticket t1") {
		t.Error("restore commit message")
	}
}
