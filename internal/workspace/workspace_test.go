package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo crée un vrai dépôt git (branche main, un commit) : ce paquet
// pilote git, le simuler ne prouverait rien.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestManager_PrepareCreatesIsolatedBranchOutsideRepo(t *testing.T) {
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}

	dir, err := m.Prepare(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !strings.HasPrefix(dir, m.Root) {
		t.Errorf("worktree %s not under %s", dir, m.Root)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
		t.Errorf("worktree does not contain the repo files: %v", err)
	}
	branch, _ := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if strings.TrimSpace(string(branch)) != "ticket/abc123" {
		t.Errorf("branch = %q, want ticket/abc123", branch)
	}
	// Idempotent : une seconde préparation (reprise, demande de
	// changements) retrouve la même copie.
	again, err := m.Prepare(context.Background(), "abc123")
	if err != nil || again != dir {
		t.Errorf("Prepare() again = %s, %v, want the same worktree", again, err)
	}
	// Le dépôt principal n'est pas touché.
	if out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); len(out) != 0 {
		t.Errorf("main repo modified: %s", out)
	}
}

func TestManager_DiffCommitAndDiscard(t *testing.T) {
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	dir, _ := m.Prepare(context.Background(), "t1")

	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644) // nouveau fichier

	diff, err := m.Diff(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+func A() {}", "b.go", "new file"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff lacks %q:\n%s", want, diff)
		}
	}

	if err := m.Commit(context.Background(), dir, "Ticket t1 : ajoute A"); err != nil {
		t.Fatal(err)
	}
	logOut, _ := exec.Command("git", "-C", repo, "log", "--oneline", "ticket/t1").Output()
	if !strings.Contains(string(logOut), "Ticket t1 : ajoute A") {
		t.Errorf("commit not on branch ticket/t1: %s", logOut)
	}
	// Le diff reste celui de la branche entière par rapport à main.
	if diff, _ := m.Diff(context.Background(), dir); !strings.Contains(diff, "+func A() {}") {
		t.Errorf("diff after commit lost the change:\n%s", diff)
	}
	mainLog, _ := exec.Command("git", "-C", repo, "log", "--oneline", "main").Output()
	if strings.Contains(string(mainLog), "ajoute A") {
		t.Error("main must not receive the commit (deployment is a separate, validated step)")
	}

	if err := m.Discard(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree still present after Discard")
	}
	if out, _ := exec.Command("git", "-C", repo, "branch", "--list", "ticket/t1").Output(); len(strings.TrimSpace(string(out))) != 0 {
		t.Errorf("branch still present after Discard: %s", out)
	}
}

func TestManager_RejectsUnsafeTicketIDs(t *testing.T) {
	m := Manager{Repo: newRepo(t), Root: t.TempDir()}
	for _, id := range []string{"../x", "a/b", "", "-rf"} {
		if _, err := m.Prepare(context.Background(), id); err == nil {
			t.Errorf("Prepare(%q) error = nil, want a refusal", id)
		}
	}
}

// Vu en réel : main avait avancé depuis la création de la copie ; le
// diff contre la pointe de main montrait ces commits à l'envers, et un
// agent qui n'avait rien modifié passait en revue avec un faux diff.
func TestManager_DiffIgnoresMainMovingOn(t *testing.T) {
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	dir, _ := m.Prepare(context.Background(), "t1")

	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package a\n\nfunc Main() {}\n"), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "main avance"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	diff, err := m.Diff(context.Background(), dir)
	if err != nil || diff != "" {
		t.Fatalf("Diff() = %q, %v ; want empty: nothing changed in the workspace", diff, err)
	}
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644)
	diff, _ = m.Diff(context.Background(), dir)
	if !strings.Contains(diff, "b.go") || strings.Contains(diff, "main.go") {
		t.Errorf("diff = %q, want only the workspace's own change", diff)
	}
}

// Une copie reprise (relance) sans travail propre repart du main actuel :
// sinon l'agent développerait sur du code périmé.
func TestManager_PrepareFastForwardsAnUntouchedWorkspace(t *testing.T) {
	repo := newRepo(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	dir, _ := m.Prepare(context.Background(), "t1")

	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package a\n"), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "main avance"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := m.Prepare(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Errorf("reused workspace not brought up to main: %v", err)
	}
}
