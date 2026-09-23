package workspace

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// withRemote : un dépôt "GitHub" local (bare) comme origin, main poussé.
func withRemote(t *testing.T) (repo, remote string) {
	t.Helper()
	repo = newRepo(t)
	remote = t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	gitIn(t, repo, "remote", "add", "origin", remote)
	gitIn(t, repo, "push", "-q", "origin", "main")
	return repo, remote
}

func TestManager_PushSendsMainAndListsWhatWillGo(t *testing.T) {
	ctx := context.Background()
	repo, remote := withRemote(t)
	m := Manager{Repo: repo, Root: t.TempDir()}
	if pending, err := m.Unpushed(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("Unpushed() = %v, %v, want nothing", pending, err)
	}
	commitFile(t, repo, "b.go", "package a\n", "Ticket t1 : ajoute b")
	commitFile(t, repo, "c.go", "package a\n", "Commit à la main")
	pending, err := m.Unpushed(ctx)
	if err != nil || len(pending) != 2 || !strings.Contains(pending[0], "Commit à la main") || !strings.Contains(pending[1], "Ticket t1") {
		t.Fatalf("Unpushed() = %q, %v", pending, err)
	}
	head, err := m.Push(ctx)
	if err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if remoteHead := gitIn(t, remote, "rev-parse", "main"); remoteHead != head || head != gitIn(t, repo, "rev-parse", "HEAD") {
		t.Errorf("remote main = %s, pushed %s", remoteHead, head)
	}
	if pending, _ := m.Unpushed(ctx); len(pending) != 0 {
		t.Errorf("still unpushed after push: %v", pending)
	}
}

// GitHub a avancé de son côté : jamais de push forcé, un refus qui
// explique quoi faire.
func TestManager_PushNeverForces(t *testing.T) {
	ctx := context.Background()
	repo, remote := withRemote(t)
	other := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	commitFile(t, other, "gh.go", "package a\n", "commit fait sur GitHub")
	gitIn(t, other, "push", "-q", "origin", "main")
	remoteBefore := gitIn(t, remote, "rev-parse", "main")

	commitFile(t, repo, "b.go", "package a\n", "local")
	m := Manager{Repo: repo, Root: t.TempDir()}
	_, err := m.Push(ctx)
	if err == nil || !strings.Contains(err.Error(), "jamais de push forcé") {
		t.Fatalf("Push() = %v, want a refusal explaining the remote moved", err)
	}
	if gitIn(t, remote, "rev-parse", "main") != remoteBefore {
		t.Error("remote main was overwritten")
	}
}

func TestManager_PushOnlyFromMain(t *testing.T) {
	repo, _ := withRemote(t)
	gitIn(t, repo, "checkout", "-q", "-b", "autre")
	m := Manager{Repo: repo, Root: t.TempDir()}
	if _, err := m.Push(context.Background()); err == nil {
		t.Error("Push() from another branch must be refused")
	}
}
