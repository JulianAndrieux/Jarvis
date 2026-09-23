package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

func devRequest(dir string) tickets.DevRequest {
	return tickets.DevRequest{Title: "Filtre", Need: "Filtrer par date", Acceptance: "?since= marche", Plan: "## Étapes\n1. ajouter Since", Dir: dir}
}

func TestDevelop_WritesRunsTestsThenFinishes(t *testing.T) {
	root := writeRepo(t)
	model := &scriptedModel{replies: []Message{
		call("c1", "read_file", `{"path": "internal/store/store.go"}`),
		call("c2", "edit_file", `{"path": "internal/store/store.go", "old": "\tLimit  int\n", "new": "\tLimit  int\n\tSince  string\n"}`),
		call("c3", "run_tests", `{"package": "./internal/store/"}`),
		call("c4", "finish", `{"summary": "Ajout de ListQuery.Since"}`),
	}}
	checker := &fakeChecker{tests: "ok  example.com/app/internal/store", ok: true}
	d := &Developer{Model: model, Checker: checker, ProjectBrief: "TDD strict."}

	var steps []tickets.AgentStep
	summary, err := d.Develop(context.Background(), devRequest(root), func(s tickets.AgentStep) { steps = append(steps, s) })
	if err != nil {
		t.Fatalf("Develop() error = %v", err)
	}
	if summary != "Ajout de ListQuery.Since" {
		t.Errorf("summary = %q", summary)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "internal/store/store.go")); !strings.Contains(string(got), "Since  string") {
		t.Error("the edit was not applied in the workspace")
	}
	if len(steps) != 3 || !strings.Contains(steps[2].Detail, "SUCCÈS") {
		t.Errorf("steps = %+v", steps)
	}
	// Outils d'écriture proposés, pas propose_plan.
	offered := map[string]bool{}
	for _, s := range model.toolSpecs[0] {
		offered[s.Name] = true
	}
	if !offered["edit_file"] || !offered["finish"] || offered["propose_plan"] {
		t.Errorf("tools offered = %v", offered)
	}
}

func TestDevelop_PromptCarriesPlanRulesAndFeedback(t *testing.T) {
	model := &scriptedModel{replies: []Message{call("c", "finish", `{"summary": "fait"}`)}}
	d := &Developer{Model: model, Checker: &fakeChecker{ok: true}, ProjectBrief: "Go, TDD."}
	req := devRequest(writeRepo(t))
	req.Feedback = "go vet : échec\nstore.go:12: unreachable code"
	d.Develop(context.Background(), req, nil)

	sys, user := model.calls[0][0].Content, model.calls[0][1].Content
	for _, want := range []string{"TDD", "run_tests", "finish", "go.mod", "Go, TDD."} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, want := range []string{"Filtrer par date", "?since= marche", "ajouter Since", "unreachable code"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message lacks %q", want)
		}
	}
}

// Les outils agissent dans la copie de travail du ticket, jamais dans le
// dépôt de l'application.
func TestDevelop_ToolsRootedInTicketWorkspace(t *testing.T) {
	ws := writeRepo(t)
	model := &scriptedModel{replies: []Message{
		call("c1", "write_file", `{"path": "nouveau.go", "content": "package app\n"}`),
		call("c2", "finish", `{"summary": "ok"}`),
	}}
	d := &Developer{Model: model, Checker: &fakeChecker{ok: true}}
	if _, err := d.Develop(context.Background(), devRequest(ws), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "nouveau.go")); err != nil {
		t.Errorf("file not written in the ticket workspace: %v", err)
	}
}

func TestDevelop_NoFinishWithinStepsIsAnError(t *testing.T) {
	var replies []Message
	for i := 0; i < 10; i++ {
		replies = append(replies, call("c", "list_files", `{"dir": "internal/`+string(rune('a'+i))+`"}`))
	}
	d := &Developer{Model: &scriptedModel{replies: replies}, Checker: &fakeChecker{ok: true}, MaxSteps: 3}
	if _, err := d.Develop(context.Background(), devRequest(writeRepo(t)), nil); err == nil || !strings.Contains(err.Error(), "3 étapes") {
		t.Errorf("err = %v", err)
	}
}

// Vu en réel : à température 0, le modèle reproduit à l'identique la
// réécriture complète coupée, malgré la consigne. Après une coupure,
// write_file n'est plus proposé : la seule voie est edit_file (et le
// contexte change, donc la réponse aussi).
func TestDevelop_AfterTruncationWriteFileIsWithdrawn(t *testing.T) {
	model := &flakyModel{failures: 1, scriptedModel: scriptedModel{replies: []Message{call("c", "finish", `{"summary": "ok"}`)}}}
	d := &Developer{Model: model, Checker: &fakeChecker{ok: true}}
	if _, err := d.Develop(context.Background(), devRequest(writeRepo(t)), nil); err != nil {
		t.Fatal(err)
	}
	after := model.scriptedModel.toolSpecs
	for _, s := range after[len(after)-1] {
		if s.Name == "write_file" {
			t.Error("write_file still offered after a truncated answer")
		}
	}
	names := map[string]bool{}
	for _, s := range after[len(after)-1] {
		names[s.Name] = true
	}
	if !names["edit_file"] || !names["finish"] {
		t.Errorf("tools after truncation = %v, want edit_file and finish kept", names)
	}
}

// Vu en réel : relancer le développement rejoue exactement le même
// déroulé (température 0). À partir de la deuxième tentative, le modèle
// est interrogé avec un peu de température pour explorer autrement.
func TestDevelop_LaterAttemptsUseSomeTemperature(t *testing.T) {
	base := &temperatureModel{}
	d := &Developer{Model: base, Checker: &fakeChecker{ok: true}}
	req := devRequest(writeRepo(t))
	d.Develop(context.Background(), req, nil)
	req.Attempt = 2
	d.Develop(context.Background(), req, nil)
	if len(base.temps) != 2 || base.temps[0] != 0 || base.temps[1] <= 0 {
		t.Errorf("temperatures used = %v, want 0 then > 0", base.temps)
	}
}

type temperatureModel struct {
	temp  float64
	temps []float64
}

func (m *temperatureModel) Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Message, error) {
	return call("f", "finish", `{"summary": "ok"}`), nil
}

func (m *temperatureModel) WithTemperature(t float64) Model {
	m.temps = append(m.temps, t)
	return m
}
