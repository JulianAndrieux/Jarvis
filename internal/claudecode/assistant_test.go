package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/mcp"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// toolCallingRunner joue Claude : il lit la configuration qu'on lui donne
// et appelle un outil du serveur avec le jeton, comme le vrai CLI.
type toolCallingRunner struct {
	req      Request
	config   []byte
	toolText string
	status   int
}

func (r *toolCallingRunner) Run(ctx context.Context, req Request, _ func(tickets.AgentStep)) (Result, error) {
	r.req = req
	r.config, _ = os.ReadFile(req.MCPConfig)
	var cfg struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	json.Unmarshal(r.config, &cfg)
	srv := cfg.Servers["jarvis"]
	call, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping_jarvis","arguments":{}}}`))
	call.Header.Set("Authorization", srv.Headers["Authorization"])
	resp, err := http.DefaultClient.Do(call)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	r.status = resp.StatusCode
	var out struct {
		Result struct {
			Content []struct{ Text string }
		}
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Result.Content) > 0 {
		r.toolText = out.Result.Content[0].Text
	}
	return Result{Text: "réponse", CostUSD: 0.01, Turns: 2}, nil
}

func TestAssistant_OffersOnlyJarvisToolsBehindAToken(t *testing.T) {
	hub := mcp.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url, err := hub.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runner := &toolCallingRunner{}
	tools := []mcp.Tool{{Name: "ping_jarvis", Call: func(context.Context, json.RawMessage) (string, error) { return "pong", nil }}}
	reply, err := Assistant{Runner: runner, Hub: hub, URL: url}.Run(ctx, assistant.Request{System: "consignes", Prompt: "question", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "réponse" || reply.CostUSD != 0.01 || reply.Turns != 2 {
		t.Errorf("réponse = %+v", reply)
	}
	// Claude a pu appeler l'outil de Jarvis pendant la session.
	if runner.status != 200 || runner.toolText != "pong" {
		t.Errorf("appel d'outil = %d %q", runner.status, runner.toolText)
	}
	req := runner.req
	if !req.NoBuiltins || !req.PromptOnStdin || req.SystemPrompt != "consignes" || req.System != "" || req.Edit || req.Prompt != "question" {
		t.Errorf("session = %+v", req)
	}
	if len(req.Tools) != 1 || req.Tools[0] != "mcp__jarvis__*" {
		t.Errorf("outils autorisés = %v", req.Tools)
	}
	// Après la réponse : le dossier de session a disparu et le jeton ne
	// vaut plus rien.
	if _, err := os.Stat(req.Dir); !os.IsNotExist(err) {
		t.Errorf("dossier de session encore là : %v", err)
	}
	var cfg struct {
		Servers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	json.Unmarshal(runner.config, &cfg)
	again, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	again.Header.Set("Authorization", cfg.Servers["jarvis"].Headers["Authorization"])
	resp, err := http.DefaultClient.Do(again)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("jeton encore valable après la réponse : %d", resp.StatusCode)
	}
}

func TestAssistant_NeedsTheToolServer(t *testing.T) {
	if _, err := (Assistant{Runner: &toolCallingRunner{}}).Run(context.Background(), assistant.Request{Prompt: "q"}); err == nil {
		t.Error("réponse sans serveur d'outils")
	}
}
