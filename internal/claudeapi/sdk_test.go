package claudeapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// apiStub : un faux point d'accès /v1/messages qui rend les corps donnés,
// dans l'ordre, et garde les requêtes.
type apiStub struct {
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	replies []string
	status  int
}

func (a *apiStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		a.mu.Lock()
		n := len(a.bodies)
		a.bodies = append(a.bodies, body)
		a.headers = append(a.headers, r.Header.Clone())
		a.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("chemin = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if a.status != 0 {
			w.WriteHeader(a.status)
			io.WriteString(w, `{"type":"error","error":{"type":"x","message":"non"}}`)
			return
		}
		io.WriteString(w, a.replies[n])
	}))
	t.Cleanup(srv.Close)
	return srv
}

const toolReply = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
 "content":[
  {"type":"thinking","thinking":"","signature":"SIG-abc"},
  {"type":"text","text":"Je regarde tes tâches."},
  {"type":"tool_use","id":"toolu_1","name":"list_tasks","input":{"filter":"week"}}],
 "stop_reason":"tool_use","stop_sequence":null,
 "usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}`

const endReply = `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5",
 "content":[{"type":"text","text":"Tu as deux tâches "},{"type":"text","text":"cette semaine."}],
 "stop_reason":"end_turn","stop_sequence":null,
 "usage":{"input_tokens":200,"output_tokens":12}}`

func newClient(t *testing.T, a *apiStub) *SDKClient {
	t.Helper()
	return NewSDKClient(Config{APIKey: "sk-test", BaseURL: a.server(t).URL, Model: "claude-opus-5-5", Effort: "medium", MaxRetries: -1})
}

func TestSDKClient_RequestShape(t *testing.T) {
	a := &apiStub{replies: []string{toolReply}}
	c := newClient(t, a)
	_, err := c.Complete(context.Background(), Request{
		System: "Tu es Jarvis.",
		Tools: []Tool{{Name: "list_tasks", Description: "Liste les tâches.", Schema: map[string]any{
			"type": "object", "properties": map[string]any{"filter": map[string]any{"type": "string"}}, "required": []string{"filter"},
		}}},
		Messages: []Message{{User: "Qu'ai-je à faire ?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, h := a.bodies[0], a.headers[0]
	// Le SDK choisit l'en-tête selon la forme de la clé.
	if h.Get("X-Api-Key") != "sk-test" && h.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("clé non transmise : %v", h)
	}
	if b["model"] != "claude-opus-5-5" {
		t.Errorf("model = %v", b["model"])
	}
	if b["max_tokens"].(float64) != DefaultMaxTokens {
		t.Errorf("max_tokens = %v", b["max_tokens"])
	}
	sys, _ := json.Marshal(b["system"])
	if !strings.Contains(string(sys), "Tu es Jarvis.") {
		t.Errorf("system = %s", sys)
	}
	if oc, _ := b["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("output_config = %v", b["output_config"])
	}
	// Repli côté serveur sur un refus : activé par défaut.
	if b["fallbacks"] != "default" || !strings.Contains(h.Get("Anthropic-Beta"), "server-side-fallback") {
		t.Errorf("fallbacks = %v, beta = %q", b["fallbacks"], h.Get("Anthropic-Beta"))
	}
	// Cache du préfixe (consigne + outils + conversation).
	if b["cache_control"] == nil {
		t.Error("cache_control absent : chaque tour relirait toute la conversation au plein tarif")
	}
	// Jamais de paramètre refusé par le modèle.
	for _, k := range []string{"temperature", "top_p", "tool_choice", "thinking"} {
		if _, ok := b[k]; ok {
			t.Errorf("%s envoyé", k)
		}
	}
	tools := b["tools"].([]any)
	tool := tools[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if tool["name"] != "list_tasks" || tool["description"] != "Liste les tâches." || schema["type"] != "object" ||
		schema["properties"].(map[string]any)["filter"] == nil || len(schema["required"].([]any)) != 1 {
		t.Errorf("tool = %v", tool)
	}
	msgs := b["messages"].([]any)
	m0, _ := json.Marshal(msgs[0])
	if len(msgs) != 1 || !strings.Contains(string(m0), `"role":"user"`) || !strings.Contains(string(m0), "Qu'ai-je à faire ?") {
		t.Errorf("messages = %s", m0)
	}
}

func TestSDKClient_ParsesToolUseAndUsage(t *testing.T) {
	a := &apiStub{replies: []string{toolReply}}
	resp, err := newClient(t, a).Complete(context.Background(), Request{Messages: []Message{{User: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != StopToolUse || resp.Text != "Je regarde tes tâches." || resp.Model != "claude-opus-5-5" {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.Calls) != 1 || resp.Calls[0].ID != "toolu_1" || resp.Calls[0].Name != "list_tasks" {
		t.Fatalf("calls = %+v", resp.Calls)
	}
	var in struct{ Filter string }
	if err := json.Unmarshal(resp.Calls[0].Input, &in); err != nil || in.Filter != "week" {
		t.Errorf("input = %s (%v)", resp.Calls[0].Input, err)
	}
	if resp.Usage != (Usage{Input: 120, Output: 30, CacheRead: 1000, CacheWrite: 50}) || resp.Usage.Total() != 1200 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if !json.Valid(resp.Raw) {
		t.Errorf("Raw illisible : %s", resp.Raw)
	}
}

// Le message du modèle est rejoué tel quel : le bloc de réflexion et sa
// signature compris, et les résultats d'outils dans un seul tour.
func TestSDKClient_ReplaysAssistantMessageUnchanged(t *testing.T) {
	a := &apiStub{replies: []string{toolReply, endReply}}
	c := newClient(t, a)
	first, err := c.Complete(context.Background(), Request{Messages: []Message{{User: "Qu'ai-je à faire ?"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Raw traverse la base : il doit survivre à un aller-retour d'octets.
	stored := json.RawMessage(append([]byte(nil), first.Raw...))
	second, err := c.Complete(context.Background(), Request{Messages: []Message{
		{User: "Qu'ai-je à faire ?"},
		{Assistant: stored},
		{Results: []Result{{CallID: "toolu_1", Content: "2 tâches", IsError: false}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Stop != StopEnd || second.Text != "Tu as deux tâches cette semaine." || len(second.Calls) != 0 {
		t.Errorf("second = %+v", second)
	}
	msgs := a.bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	asst := msgs[1].(map[string]any)
	blocks := asst["content"].([]any)
	if asst["role"] != "assistant" || len(blocks) != 3 {
		t.Fatalf("assistant rejoué = %v", asst)
	}
	th := blocks[0].(map[string]any)
	if th["type"] != "thinking" || th["signature"] != "SIG-abc" {
		t.Errorf("bloc de réflexion altéré : %v", th)
	}
	tu := blocks[2].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "toolu_1" || tu["input"].(map[string]any)["filter"] != "week" {
		t.Errorf("tool_use altéré : %v", tu)
	}
	res := msgs[2].(map[string]any)
	rb := res["content"].([]any)[0].(map[string]any)
	if res["role"] != "user" || rb["type"] != "tool_result" || rb["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_result = %v", res)
	}
	rj, _ := json.Marshal(rb)
	if !strings.Contains(string(rj), "2 tâches") {
		t.Errorf("contenu du résultat absent : %s", rj)
	}
}

func TestSDKClient_ToolErrorIsFlagged(t *testing.T) {
	a := &apiStub{replies: []string{toolReply, endReply}}
	c := newClient(t, a)
	first, _ := c.Complete(context.Background(), Request{Messages: []Message{{User: "x"}}})
	if _, err := c.Complete(context.Background(), Request{Messages: []Message{
		{User: "x"}, {Assistant: first.Raw}, {Results: []Result{{CallID: "toolu_1", Content: "introuvable", IsError: true}}},
	}}); err != nil {
		t.Fatal(err)
	}
	rb := a.bodies[1]["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if rb["is_error"] != true {
		t.Errorf("is_error = %v", rb["is_error"])
	}
}

func TestSDKClient_Refusal(t *testing.T) {
	a := &apiStub{replies: []string{`{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],
	 "stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"non"},
	 "usage":{"input_tokens":1,"output_tokens":0}}`}}
	resp, err := newClient(t, a).Complete(context.Background(), Request{Messages: []Message{{User: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != StopRefusal || resp.Refusal != "cyber" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestSDKClient_ErrorsAreClassified(t *testing.T) {
	for status, want := range map[int]error{401: ErrAuth, 403: ErrAuth, 429: ErrRateLimited, 529: ErrOverloaded, 500: ErrOverloaded} {
		a := &apiStub{status: status}
		_, err := newClient(t, a).Complete(context.Background(), Request{Messages: []Message{{User: "x"}}})
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d : err = %v, veut %v", status, err, want)
		}
	}
	a := &apiStub{status: 400}
	_, err := newClient(t, a).Complete(context.Background(), Request{Messages: []Message{{User: "x"}}})
	if err == nil || errors.Is(err, ErrAuth) || errors.Is(err, ErrRateLimited) || errors.Is(err, ErrOverloaded) {
		t.Errorf("HTTP 400 : err = %v", err)
	}
}

func TestSDKClient_RejectsMalformedMessage(t *testing.T) {
	c := newClient(t, &apiStub{replies: []string{endReply}})
	for _, m := range []Message{{}, {User: "a", Results: []Result{{CallID: "x"}}}, {Assistant: json.RawMessage(`{"role":`)}} {
		if _, err := c.Complete(context.Background(), Request{Messages: []Message{m}}); err == nil {
			t.Errorf("message %+v accepté", m)
		}
	}
}

func TestFakeClient(t *testing.T) {
	f := &FakeClient{Responses: []Response{{Text: "a"}}, Errs: []error{nil, ErrOverloaded}}
	if r, err := f.Complete(context.Background(), Request{System: "s"}); err != nil || r.Text != "a" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := f.Complete(context.Background(), Request{}); !errors.Is(err, ErrOverloaded) {
		t.Errorf("err = %v", err)
	}
	if _, err := f.Complete(context.Background(), Request{}); !errors.Is(err, ErrFakeExhausted) {
		t.Errorf("err = %v", err)
	}
	if got := f.Requests(); len(got) != 3 || got[0].System != "s" {
		t.Errorf("requests = %+v", got)
	}
}
