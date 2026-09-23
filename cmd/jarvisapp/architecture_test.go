package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/projectinfo"
)

const archClaudeMD = `# Projet

## Contraintes non négociables
- Langage : **Go**.

## État des jalons
- **Jalon 1 — squelette CLI : fait.**
  - détail <b>du</b> jalon 1
- **Jalon 2 — ports modèles : fait côté code.**

## Décisions tranchées
- **Moteur : llama.cpp server**.
`

// newArchServer : un vrai dépôt git avec un CLAUDE.md, deux commits.
func newArchServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Andri", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=Andri", "GIT_COMMITTER_EMAIL=a@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(archClaudeMD), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "Jalon 1: squelette CLI\n\n- corps <script>x</script>")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "Jalon 2: ports modèles")
	s, _ := newTestServer(t, nil)
	s.ModuleDir = dir
	return s, dir
}

func TestArchitecturePage_DecisionsMilestonesAndCommits(t *testing.T) {
	s, _ := newArchServer(t)
	rec := get(t, s, "/admin/architecture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Décisions tranchées", "<strong>Moteur : llama.cpp server</strong>", // décisions, Markdown rendu
		"Contraintes non négociables",
		"squelette CLI", "ports modèles", "fait côté code.", // jalons
		"détail &lt;b&gt;du&lt;/b&gt; jalon 1",             // échappé
		"Jalon 1: squelette CLI", "Jalon 2: ports modèles", // commits
		`hx-get="/admin/architecture/infra"`, `hx-get="/admin/architecture/version?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>x</script>") || strings.Contains(body, "<b>du</b>") {
		t.Error("commit message or CLAUDE.md rendered unescaped")
	}
	// Les jalons les plus récents d'abord, chacun avec ses commits.
	if strings.Index(body, "ports modèles</span>") > strings.Index(body, "squelette CLI</span>") {
		t.Error("milestones should be listed newest first")
	}
}

func TestArchitectureVersion_UnchangedThenChanged(t *testing.T) {
	s, dir := newArchServer(t)
	v, err := projectinfo.Fingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, s, "/admin/architecture/version?v="+v); rec.Code != http.StatusNoContent || rec.Header().Get("HX-Refresh") != "" {
		t.Errorf("unchanged: %d %v", rec.Code, rec.Header())
	}
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(archClaudeMD+"\n- nouvelle décision\n"), 0o644)
	if rec := get(t, s, "/admin/architecture/version?v="+v); rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("changed: %d %v", rec.Code, rec.Header())
	}
}

func TestArchitectureCommitDiff(t *testing.T) {
	s, dir := newArchServer(t)
	commits, _ := projectinfo.Commits(context.Background(), dir, 0)
	rec := get(t, s, "/admin/architecture/commits/"+commits[0].Hash)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "A() {}") || !strings.Contains(rec.Body.String(), "a.go") {
		t.Errorf("diff: %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, s, "/admin/architecture/commits/HEAD"); rec.Code != http.StatusBadRequest {
		t.Errorf("non-hash revision: status %d, want 400", rec.Code)
	}
}

func TestArchitectureInfra_ProbesAndDraws(t *testing.T) {
	s, _ := newArchServer(t)
	s.Infra = projectinfo.Diagram{
		Nodes: []projectinfo.Node{
			{ID: "app", Title: "jarvisapp", Col: 0, Check: func(context.Context) projectinfo.Status {
				return projectinfo.Status{Health: projectinfo.Up, Detail: "2 en attente"}
			}},
			{ID: "vlm", Title: "VLM", Col: 1, Check: func(context.Context) projectinfo.Status {
				return projectinfo.Status{Health: projectinfo.Down, Detail: "injoignable"}
			}},
		},
		Edges: []projectinfo.Edge{{From: "app", To: "vlm", Label: "pages"}},
	}
	rec := get(t, s, "/admin/architecture/infra")
	body := rec.Body.String()
	for _, want := range []string{"<svg", "2 en attente", "injoignable", `class="node down"`, "1 composant en panne"} {
		if !strings.Contains(body, want) {
			t.Errorf("infra fragment lacks %q", want)
		}
	}
}

func TestLayout_NavHasArchitecture(t *testing.T) {
	s, _ := newArchServer(t)
	if body := get(t, s, "/admin/architecture").Body.String(); !strings.Contains(body, `href="/admin/architecture" class="active"`) {
		t.Error("nav lacks an active Architecture tab")
	}
}
