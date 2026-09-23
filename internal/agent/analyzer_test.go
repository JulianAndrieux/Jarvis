package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// scriptedModel rejoue des réponses dans l'ordre et enregistre les
// conversations reçues.
type scriptedModel struct {
	mu        sync.Mutex
	replies   []Message
	err       error
	calls     [][]Message
	toolSpecs [][]ToolSpec
}

func (m *scriptedModel) Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, append([]Message(nil), msgs...))
	m.toolSpecs = append(m.toolSpecs, tools)
	if m.err != nil {
		return Message{}, m.err
	}
	if len(m.replies) == 0 {
		return Message{Role: "assistant"}, nil
	}
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

func call(id, name, args string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: name, Arguments: args}}}
}

func request() tickets.AnalysisRequest {
	return tickets.AnalysisRequest{Title: "Filtre par date", Need: "Filtrer les documents par date d'import", Acceptance: "?since=2026-09-01 ne liste que les documents importés depuis"}
}

func TestAnalyze_InvestigatesThenProposesPlan(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("c1", "search", `{"pattern": "ListQuery"}`),
		call("c2", "read_file", `{"path": "internal/store/store.go"}`),
		call("c3", "propose_plan", `{"plan": "## Étapes\n1. ajouter ListQuery.Since"}`),
	}}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}, ProjectBrief: "Jarvis : pipeline Go."}

	var steps []tickets.AgentStep
	plan, err := a.Analyze(context.Background(), request(), func(s tickets.AgentStep) { steps = append(steps, s) })
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if plan != "## Étapes\n1. ajouter ListQuery.Since" {
		t.Errorf("plan = %q", plan)
	}
	if len(steps) != 2 || !strings.Contains(steps[0].Summary, "Cherche « ListQuery »") || !strings.Contains(steps[1].Detail, "type ListQuery struct") {
		t.Errorf("steps = %+v, want the search then the read, with their outputs", steps)
	}

	// Chaque sortie d'outil revient au modèle, rattachée à son appel.
	last := model.calls[2]
	toolMsg := last[len(last)-1]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "c2" || !strings.Contains(toolMsg.Content, "type ListQuery struct") {
		t.Errorf("last message sent = %+v, want the read_file result for c2", toolMsg)
	}
	// Seuls les outils en lecture seule sont proposés.
	for _, spec := range model.toolSpecs[0] {
		if spec.Name != "list_files" && spec.Name != "read_file" && spec.Name != "search" && spec.Name != "propose_plan" {
			t.Errorf("unexpected tool offered: %s", spec.Name)
		}
	}
}

func TestAnalyze_PromptCarriesProjectRulesTicketAndRevision(t *testing.T) {
	model := &scriptedModel{replies: []Message{call("c1", "propose_plan", `{"plan": "p"}`)}}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}, ProjectBrief: "TDD strict, Go."}
	req := request()
	req.PreviousPlan, req.Feedback = "ancien plan", "utilise plutôt un paramètre until aussi"

	if _, err := a.Analyze(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	sys, user := model.calls[0][0], model.calls[0][1]
	for _, want := range []string{"TDD strict, Go.", "lecture seule", "propose_plan"} {
		if sys.Role != "system" || !strings.Contains(sys.Content, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, want := range []string{"Filtre par date", "Filtrer les documents", "?since=2026-09-01", "ancien plan", "until aussi"} {
		if !strings.Contains(user.Content, want) {
			t.Errorf("user message lacks %q:\n%s", want, user.Content)
		}
	}
}

// Un modèle qui répond directement en texte (sans appeler propose_plan)
// voit sa réponse retenue comme plan, si elle est substantielle.
func TestAnalyze_PlainTextAnswerAcceptedAsPlan(t *testing.T) {
	text := "## Compréhension\nIl faut ajouter un filtre de date à la liste des documents, côté store et côté route."
	model := &scriptedModel{replies: []Message{{Role: "assistant", Content: text}}}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}}
	plan, err := a.Analyze(context.Background(), request(), nil)
	if err != nil || plan != text {
		t.Errorf("plan = %q err = %v", plan, err)
	}
}

func TestAnalyze_StopsAfterMaxStepsWithAClearError(t *testing.T) {
	var replies []Message
	for i := 0; i < 20; i++ {
		replies = append(replies, call("c", "list_files", `{"dir": ""}`))
	}
	model := &scriptedModel{replies: replies}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}, MaxSteps: 4}
	_, err := a.Analyze(context.Background(), request(), nil)
	if err == nil || !strings.Contains(err.Error(), "4 étapes") {
		t.Errorf("err = %v, want an error naming the step limit", err)
	}
	// Dernière chance : seul propose_plan est alors proposé.
	lastTools := model.toolSpecs[len(model.toolSpecs)-1]
	if len(lastTools) != 1 || lastTools[0].Name != "propose_plan" {
		t.Errorf("final turn tools = %+v, want only propose_plan", lastTools)
	}
}

func TestAnalyze_ModelErrorPropagates(t *testing.T) {
	a := &Analyzer{Model: &scriptedModel{err: errors.New("llm down")}, Tools: Tools{Root: writeRepo(t)}}
	if _, err := a.Analyze(context.Background(), request(), nil); err == nil || !strings.Contains(err.Error(), "llm down") {
		t.Errorf("err = %v", err)
	}
}

// Le contexte du modèle local est petit : les anciennes sorties d'outils
// sont résumées pour que la conversation reste sous le budget, la plus
// récente reste intacte.
func TestAnalyze_CompactsOldToolOutputsToStayWithinBudget(t *testing.T) {
	var replies []Message
	for i := 0; i < 6; i++ { // lectures distinctes : un appel répété n'est plus réexécuté
		replies = append(replies, call("c", "read_file", fmt.Sprintf(`{"path": "internal/store/secret.txt", "start_line": %d}`, i+1)))
	}
	replies = append(replies, call("p", "propose_plan", `{"plan": "p"}`))
	model := &scriptedModel{replies: replies}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}, ContextChars: 6000, MaxSteps: 10}

	if _, err := a.Analyze(context.Background(), request(), nil); err != nil {
		t.Fatal(err)
	}
	for i, msgs := range model.calls {
		if n := size(msgs); n > 6000+3000 { // tolérance : la dernière sortie (tronquée à 3000) reste entière
			t.Errorf("call %d sent %d chars, want the history compacted near the 6000 budget", i, n)
		}
	}
	last := model.calls[len(model.calls)-1]
	if !strings.Contains(last[len(last)-1].Content, "ligne") {
		t.Error("the most recent tool output must stay intact")
	}
}

// Un appel identique à un appel précédent n'est pas réexécuté : le
// modèle est prévenu qu'il tourne en rond (vu en réel : 8 fois le même
// read_file en échec jusqu'à la limite d'étapes).
func TestAnalyze_RepeatedIdenticalCallIsFlaggedNotReexecuted(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("c1", "read_file", `{"path": "absent.html"}`),
		call("c2", "read_file", `{"path": "absent.html"}`),
		call("c3", "propose_plan", `{"plan": "plan après correction de trajectoire"}`),
	}}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}}
	var steps []tickets.AgentStep
	if _, err := a.Analyze(context.Background(), request(), func(s tickets.AgentStep) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	third := model.calls[2]
	repeated := third[len(third)-1]
	if repeated.Role != "tool" || !strings.Contains(repeated.Content, "déjà fait") {
		t.Errorf("reply to the repeated call = %+v, want a note that it was already done", repeated)
	}
	if len(steps) != 2 || !strings.Contains(steps[1].Summary, "répété") {
		t.Errorf("steps = %+v, want the repetition visible in the ticket thread", steps)
	}
}

func TestAnalyze_SystemPromptExplainsCodeConventions(t *testing.T) {
	model := &scriptedModel{replies: []Message{call("c", "propose_plan", `{"plan": "p"}`)}}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}}
	a.Analyze(context.Background(), request(), nil)
	sys := model.calls[0][0].Content
	for _, want := range []string{"identifiants", "anglais", ".templ"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}

// Vu en réel : une réponse coupée (appel d'outil au JSON invalide) fait
// renvoyer une erreur 500 par llama.cpp. Ce n'est pas fatal : le modèle
// est prévenu et réessaie, dans la limite de quelques fois.
type flakyModel struct {
	scriptedModel
	failures int
}

func (m *flakyModel) Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Message, error) {
	if m.failures > 0 {
		m.failures--
		m.mu.Lock()
		m.calls = append(m.calls, append([]Message(nil), msgs...))
		m.mu.Unlock()
		return Message{}, errors.New(`agent: server returned 500: {"error":{"code":500,"message":"Failed to parse tool call arguments as JSON: parse error"}}`)
	}
	return m.scriptedModel.Chat(ctx, msgs, tools)
}

func TestLoop_TruncatedToolCallIsRecoverable(t *testing.T) {
	model := &flakyModel{failures: 1, scriptedModel: scriptedModel{replies: []Message{call("c", "propose_plan", `{"plan": "plan après coupure"}`)}}}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}}
	var steps []tickets.AgentStep
	plan, err := a.Analyze(context.Background(), request(), func(s tickets.AgentStep) { steps = append(steps, s) })
	if err != nil || plan != "plan après coupure" {
		t.Fatalf("plan = %q err = %v, want recovery after a truncated call", plan, err)
	}
	retry := model.calls[1]
	if !strings.Contains(retry[len(retry)-1].Content, "coupée") {
		t.Errorf("the model should be told its answer was cut: %+v", retry[len(retry)-1])
	}
	if len(steps) != 1 || !strings.Contains(steps[0].Summary, "coupée") {
		t.Errorf("steps = %+v, want the truncation visible in the thread", steps)
	}
}

func TestLoop_RepeatedTruncationsEventuallyFail(t *testing.T) {
	model := &flakyModel{failures: 10}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}}
	if _, err := a.Analyze(context.Background(), request(), nil); err == nil || !strings.Contains(err.Error(), "Failed to parse") {
		t.Errorf("err = %v, want the parse error after too many truncations", err)
	}
}

func TestSummarize_MissingPathIsExplicit(t *testing.T) {
	if got := summarize(ToolCall{Name: "write_file", Arguments: `{"content": "x"}`}); got != "Écrit (chemin manquant)" {
		t.Errorf("summarize = %q", got)
	}
}

// Vu en réel : 20 recherches d'identifiants inventés, sans résultat,
// jusqu'à la limite d'étapes. Après plusieurs appels stériles d'affilée,
// le modèle est recadré ; s'il continue, la boucle s'arrête tôt avec une
// raison claire au lieu d'épuiser les étapes.
func TestLoop_StagnationIsSteeredThenStopped(t *testing.T) {
	var replies []Message
	for i := 0; i < 30; i++ {
		replies = append(replies, call("c", "search", fmt.Sprintf(`{"pattern": "InventeNom%d"}`, i)))
	}
	model := &scriptedModel{replies: replies}
	a := &Analyzer{Model: model, Tools: Tools{Root: writeRepo(t)}, MaxSteps: 40}
	var steps []tickets.AgentStep
	_, err := a.Analyze(context.Background(), request(), func(s tickets.AgentStep) { steps = append(steps, s) })
	if err == nil || !strings.Contains(err.Error(), "tourne en rond") {
		t.Fatalf("err = %v, want an early stop for stagnation", err)
	}
	if len(model.calls) >= 30 {
		t.Errorf("model called %d times, want an early stop", len(model.calls))
	}
	steered := false
	for _, msgs := range model.calls {
		for _, m := range msgs {
			steered = steered || (m.Role == "user" && strings.Contains(m.Content, "n'ont rien donné"))
		}
	}
	if !steered {
		t.Error("the model should be steered before being stopped")
	}
}
