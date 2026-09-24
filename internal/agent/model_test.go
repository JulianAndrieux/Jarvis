package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPModel_SendsToolsAndHistoryParsesToolCalls(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"x1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()

	m := HTTPModel{BaseURL: srv.URL + "/v1/", Model: "qwen3-8b"}
	reply, err := m.Chat(context.Background(), []Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a1", Name: "search", Arguments: `{"pattern":"x"}`}}},
		{Role: "tool", ToolCallID: "a1", Content: "résultat"},
	}, ReadOnlySpecs())
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Name != "read_file" || reply.ToolCalls[0].ID != "x1" || reply.ToolCalls[0].Arguments != `{"path":"go.mod"}` {
		t.Errorf("reply = %+v", reply)
	}

	if got["model"] != "qwen3-8b" || got["temperature"] != 0.0 {
		t.Errorf("model/temperature = %v/%v", got["model"], got["temperature"])
	}
	tools := got["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if tools[0].(map[string]any)["type"] != "function" || fn["name"] != "list_files" || fn["parameters"] == nil {
		t.Errorf("tools[0] = %v", tools[0])
	}
	msgs := got["messages"].([]any)
	assistant := msgs[1].(map[string]any)
	tc := assistant["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "a1" || tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "search" {
		t.Errorf("assistant tool_calls = %v", assistant["tool_calls"])
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "a1" || tool["content"] != "résultat" {
		t.Errorf("tool message = %v", tool)
	}
}

func TestHTTPModel_ServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Context size has been exceeded."}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := HTTPModel{BaseURL: srv.URL, Model: "m"}.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "Context size") {
		t.Errorf("err = %v", err)
	}
}

// Vu en réel : sans limite, une réécriture complète qui s'emballe a
// généré pendant 6 minutes jusqu'à saturer le contexte. MaxTokens borne
// chaque réponse ; 0 : pas de limite envoyée.
func TestHTTPModel_SendsMaxTokens(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = nil
		json.Unmarshal(body, &got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	HTTPModel{BaseURL: srv.URL + "/v1", Model: "m", MaxTokens: 2048}.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if got["max_tokens"] != 2048.0 {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
	HTTPModel{BaseURL: srv.URL + "/v1", Model: "m"}.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if _, ok := got["max_tokens"]; ok {
		t.Error("max_tokens sent although unset")
	}
}
