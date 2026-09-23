package projectinfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Andri", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=Andri", "GIT_COMMITTER_EMAIL=a@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "Jalon 1 : squelette CLI\n\n- premier point\n- caractère spécial | et ¤")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	run("commit", "-q", "-am", "Jalons 26-27 : outil de tickets")
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "Lanceur : PATH complet")
	return dir
}

func TestCommits_NewestFirstWithBodyAndMilestone(t *testing.T) {
	commits, err := Commits(context.Background(), gitRepo(t), 0)
	if err != nil {
		t.Fatalf("Commits() error = %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	if commits[0].Subject != "Lanceur : PATH complet" || commits[2].Subject != "Jalon 1 : squelette CLI" {
		t.Errorf("order = %q ... %q, want newest first", commits[0].Subject, commits[2].Subject)
	}
	first := commits[2]
	if !strings.Contains(first.Body, "- premier point") || !strings.Contains(first.Body, "caractère spécial | et ¤") {
		t.Errorf("body = %q", first.Body)
	}
	if first.Author != "Andri" || len(first.Hash) != 40 || len(first.Short) < 7 || first.Date.IsZero() {
		t.Errorf("commit meta = %+v", first)
	}
	if first.Milestone != "Jalon 1" || commits[1].Milestone != "Jalons 26-27" || commits[0].Milestone != "" {
		t.Errorf("milestones = %q, %q, %q", first.Milestone, commits[1].Milestone, commits[0].Milestone)
	}
}

func TestCommits_Limit(t *testing.T) {
	commits, _ := Commits(context.Background(), gitRepo(t), 2)
	if len(commits) != 2 {
		t.Errorf("got %d commits, want 2", len(commits))
	}
}

func TestShow_DiffOfACommit(t *testing.T) {
	dir := gitRepo(t)
	commits, _ := Commits(context.Background(), dir, 0)
	c, diff, err := Show(context.Background(), dir, commits[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "Jalons 26-27 : outil de tickets" || !strings.Contains(diff, "+func A() {}") || !strings.Contains(diff, "diff --git a/a.go b/a.go") {
		t.Errorf("Show() = %+v\n%s", c, diff)
	}
}

// Le hash vient de l'URL : jamais passé à git s'il n'est pas un hash.
func TestShow_RejectsAnythingButAHash(t *testing.T) {
	dir := gitRepo(t)
	for _, bad := range []string{"", "HEAD", "--output=/tmp/x", "abc; rm -rf /", "../../etc", strings.Repeat("g", 40)} {
		if _, _, err := Show(context.Background(), dir, bad); err == nil {
			t.Errorf("Show(%q) error = nil, want a refusal", bad)
		}
	}
}

func TestWorkingChanges(t *testing.T) {
	dir := gitRepo(t)
	if got, err := WorkingChanges(context.Background(), dir); err != nil || len(got) != 0 {
		t.Fatalf("clean repo: %v, %v", got, err)
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // modifié\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "nouveau fichier.go"), []byte("package a\n"), 0o644)
	got, err := WorkingChanges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{Status: "M", Path: "a.go"}, {Status: "??", Path: "nouveau fichier.go"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("WorkingChanges() = %+v, want %+v", got, want)
	}
}

// L'empreinte change avec un commit, une modification non commitée ou
// CLAUDE.md — c'est ce que la page sonde pour se mettre à jour.
func TestFingerprint(t *testing.T) {
	dir := gitRepo(t)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# v1\n"), 0o644)
	ctx := context.Background()
	f1, err := Fingerprint(ctx, dir)
	if err != nil || f1 == "" {
		t.Fatalf("Fingerprint() = %q, %v", f1, err)
	}
	if f2, _ := Fingerprint(ctx, dir); f2 != f1 {
		t.Error("fingerprint must be stable")
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // x\n"), 0o644)
	f3, _ := Fingerprint(ctx, dir)
	if f3 == f1 {
		t.Error("fingerprint ignores uncommitted changes")
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // y\n"), 0o644)
	if f4, _ := Fingerprint(ctx, dir); f4 == f3 {
		t.Error("fingerprint ignores further edits of an already modified file")
	}
}
