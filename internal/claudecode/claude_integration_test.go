//go:build claude

package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Avec le vrai Claude Code (go test -tags=claude, payant) : un mini ticket
// développé dans un module jetable, puis relu — permissions (édition,
// go test) et flux réels.
func TestRealClaude_DevelopsAndReviews(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude introuvable")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "calc.go"), []byte("package demo\n"), 0o644)
	var steps []string
	onStep := func(s tickets.AgentStep) { steps = append(steps, s.Summary) }
	cli := CLI{}
	summary, err := Developer{Runner: cli}.Develop(context.Background(), tickets.DevRequest{
		Title: "Addition", Need: "Une fonction Add(a, b int) int dans calc.go.", Acceptance: "Add(2, 3) == 5",
		Plan: "1. Écrire calc_test.go (TestAdd)\n2. Écrire Add dans calc.go", Dir: dir, Attempt: 1,
	}, onStep)
	if err != nil {
		t.Fatalf("Develop: %v\nsteps: %q", err, steps)
	}
	t.Logf("summary: %s\nsteps: %q", summary, steps)
	code, _ := os.ReadFile(filepath.Join(dir, "calc.go"))
	if !strings.Contains(string(code), "func Add") {
		t.Fatalf("calc.go = %s", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "calc_test.go")); err != nil {
		t.Error("no test written")
	}
	if out, err := exec.Command("go", "test", "./...").CombinedOutput(); err != nil {
		t.Errorf("go test: %v\n%s", err, out)
	}
	res, err := Reviewer{Runner: cli}.Review(context.Background(), tickets.ReviewRequest{
		Title: "Addition", Need: "Add", Plan: "1. test 2. Add", Diff: "+++ b/calc.go\n+" + string(code), Dir: dir,
	}, func(tickets.AgentStep) {})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	t.Logf("review: %+v", res)
}
