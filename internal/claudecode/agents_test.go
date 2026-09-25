package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// fakeRunner garde la requête et rend un résultat fixé.
type fakeRunner struct {
	res Result
	err error
	got Request
}

func (f *fakeRunner) Run(ctx context.Context, req Request, onStep func(tickets.AgentStep)) (Result, error) {
	f.got = req
	onStep(tickets.AgentStep{Summary: "Read x.go"})
	return f.res, f.err
}

func writes(tools []string) bool {
	for _, t := range tools {
		if strings.HasPrefix(t, "Edit") || strings.HasPrefix(t, "Write") || strings.HasPrefix(t, "Bash") {
			return true
		}
	}
	return false
}

// Chaque outil de fichier est borné au dossier de travail : « Read » seul
// lit toute la machine (vérifié avec le vrai CLI : /etc/hosts lu).
func TestTools_ConfinedToTheWorkingDirectory(t *testing.T) {
	for _, tools := range [][]string{readOnlyTools, devTools} {
		for _, tool := range tools {
			name, _, _ := strings.Cut(tool, "(")
			switch name {
			case "Read", "Glob", "Grep", "Edit", "Write":
				if tool != name+"(./**)" {
					t.Errorf("%s is not confined to ./**", tool)
				}
			}
		}
	}
}

func TestAnalyst_ReadOnlyPlanFromTheTicket(t *testing.T) {
	r := &fakeRunner{res: Result{Text: "\n## Étapes\n1. x\n"}}
	released := 0
	a := Analyst{Runner: r, Snapshot: func(ctx context.Context) (string, func(), error) {
		return "/snap", func() { released++ }, nil
	}}
	var steps int
	plan, err := a.Analyze(context.Background(), tickets.AnalysisRequest{
		Title: "Filtre par date", Need: "filtrer", Acceptance: "?since marche", PreviousPlan: "ancien plan", Feedback: "- plus simple",
	}, func(tickets.AgentStep) { steps++ })
	if err != nil || plan != "## Étapes\n1. x" || steps != 1 {
		t.Fatalf("plan = %q, %v, steps %d", plan, err, steps)
	}
	if r.got.Dir != "/snap" || r.got.Edit || writes(r.got.Tools) || len(r.got.Tools) == 0 {
		t.Errorf("request = %+v, want read-only tools in the snapshot", r.got)
	}
	if released != 1 {
		t.Errorf("snapshot released %d times, want once", released)
	}
	for _, want := range []string{"Filtre par date", "filtrer", "?since marche", "ancien plan", "plus simple"} {
		if !strings.Contains(r.got.Prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	r.res.Text = "  "
	if _, err := a.Analyze(context.Background(), tickets.AnalysisRequest{Title: "t"}, func(tickets.AgentStep) {}); err == nil {
		t.Error("empty plan accepted")
	}
	a.Snapshot = func(ctx context.Context) (string, func(), error) { return "", nil, errors.New("git cassé") }
	if _, err := a.Analyze(context.Background(), tickets.AnalysisRequest{Title: "t"}, func(tickets.AgentStep) {}); err == nil || !strings.Contains(err.Error(), "git cassé") {
		t.Errorf("snapshot error = %v", err)
	}
	a.Snapshot = func(ctx context.Context) (string, func(), error) { return "/snap", func() {}, nil }
	r.err = errors.New("quota")
	if _, err := a.Analyze(context.Background(), tickets.AnalysisRequest{Title: "t"}, func(tickets.AgentStep) {}); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Errorf("err = %v", err)
	}
}

func TestDeveloper_EditsInTheWorkspace(t *testing.T) {
	r := &fakeRunner{res: Result{Text: "Tout est vert.\n---\nFiltre ajouté.", Structured: []byte(`{"summary":"Filtre ajouté, avec ses tests."}`)}}
	d := Developer{Runner: r}
	summary, err := d.Develop(context.Background(), tickets.DevRequest{
		Title: "Filtre", Need: "filtrer", Plan: "## Étapes\n1. x", Feedback: "--- FAIL: TestSince", Dir: "/ws/t1", Attempt: 2,
	}, func(tickets.AgentStep) {})
	if err != nil || summary != "Filtre ajouté, avec ses tests." {
		t.Fatalf("summary = %q, %v", summary, err)
	}
	if r.got.Dir != "/ws/t1" || !r.got.Edit || len(r.got.Schema) == 0 {
		t.Errorf("request = %+v, want edits in the ticket workspace", r.got)
	}
	for _, want := range []string{"Edit(./**)", "Write(./**)", "Bash(go test:*)", "Bash(gofmt:*)"} {
		if !slices.Contains(r.got.Tools, want) {
			t.Errorf("tools %v lack %s", r.got.Tools, want)
		}
	}
	for _, unwanted := range []string{"Bash(git commit:*)", "Bash(git push:*)", "Bash"} {
		if slices.Contains(r.got.Tools, unwanted) {
			t.Errorf("tools %v contain %s", r.got.Tools, unwanted)
		}
	}
	for _, want := range []string{"## Étapes", "--- FAIL: TestSince", "tentative 2", "commit"} {
		if !strings.Contains(r.got.Prompt+r.got.System, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// Sans résumé structuré, le texte final sert encore de résumé.
func TestDeveloper_SummaryFallsBackToText(t *testing.T) {
	r := &fakeRunner{res: Result{Text: "  Fait.  "}}
	if s, err := (Developer{Runner: r}).Develop(context.Background(), tickets.DevRequest{Dir: "/ws"}, func(tickets.AgentStep) {}); err != nil || s != "Fait." {
		t.Errorf("summary = %q, %v", s, err)
	}
}

func TestReviewer_StructuredVerdict(t *testing.T) {
	verdict := map[string]any{"approved": true, "summary": "Correct.", "issues": []map[string]any{
		{"file": "store.go", "line": 12, "severity": "mineur", "message": "nom peu clair"},
	}}
	b, _ := json.Marshal(verdict)
	r := &fakeRunner{res: Result{Structured: b}}
	rv := Reviewer{Runner: r}
	res, err := rv.Review(context.Background(), tickets.ReviewRequest{Title: "Filtre", Plan: "plan", Diff: "+++ b/store.go\n+func Since()", Dir: "/ws/t1"}, func(tickets.AgentStep) {})
	if err != nil || !res.Approved || res.Summary != "Correct." || len(res.Issues) != 1 || res.Issues[0].Line != 12 {
		t.Fatalf("review = %+v, %v", res, err)
	}
	if r.got.Dir != "/ws/t1" || r.got.Edit || writes(r.got.Tools) || len(r.got.Schema) == 0 || !strings.Contains(r.got.Prompt, "func Since()") {
		t.Errorf("request = %+v", r.got)
	}

	// Une remarque bloquante l'emporte sur approved.
	verdict["issues"] = []map[string]any{{"file": "store.go", "severity": "bloquant", "message": "panique sur nil"}}
	b, _ = json.Marshal(verdict)
	r.res = Result{Structured: b}
	if res, _ := rv.Review(context.Background(), tickets.ReviewRequest{Diff: "+x"}, func(tickets.AgentStep) {}); res.Approved {
		t.Error("approved despite a blocking issue")
	}
	r.res = Result{Text: "pas de verdict"}
	if _, err := rv.Review(context.Background(), tickets.ReviewRequest{Diff: "+x"}, func(tickets.AgentStep) {}); err == nil {
		t.Error("missing verdict accepted")
	}
}
