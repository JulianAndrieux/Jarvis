package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func echoServer() Server {
	return Server{Name: "jarvis", Tools: []Tool{
		{
			Name: "echo", Description: "Répète le texte.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct{ Text string }
				json.Unmarshal(args, &in)
				return "écho : " + in.Text, nil
			},
		},
		{Name: "broken", Call: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("introuvable") }},
		{Name: "panics", Call: func(context.Context, json.RawMessage) (string, error) { panic("aïe") }},
	}}
}

// rpc poste une requête JSON-RPC et rend la réponse décodée.
func rpc(t *testing.T, h http.Handler, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var out map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("réponse illisible : %s", raw)
		}
	}
	return rec.Code, out
}

func TestServer_InitializeEchoesProtocolVersion(t *testing.T) {
	code, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	res, _ := out["result"].(map[string]any)
	if code != 200 || res["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize = %d %v", code, out)
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capacité tools absente : %v", res)
	}
	if out["id"] != float64(1) {
		t.Errorf("id non repris : %v", out["id"])
	}
}

func TestServer_ListsTools(t *testing.T) {
	_, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":"a","method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools = %v", tools)
	}
	first := tools[0].(map[string]any)
	if first["name"] != "echo" || first["description"] != "Répète le texte." || first["inputSchema"] == nil {
		t.Errorf("premier outil = %v", first)
	}
	// Un outil sans schéma en reçoit un vide : le client exige un objet.
	if s := tools[1].(map[string]any)["inputSchema"].(map[string]any); s["type"] != "object" {
		t.Errorf("schéma par défaut = %v", s)
	}
}

func TestServer_CallsTool(t *testing.T) {
	_, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"salut"}}}`)
	res := out["result"].(map[string]any)
	content := res["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "écho : salut" || res["isError"] == true {
		t.Errorf("résultat = %v", res)
	}
}

// Une erreur d'outil est un résultat (le modèle la lit et s'adapte), pas
// une erreur de protocole.
func TestServer_ToolErrorIsAResult(t *testing.T) {
	for _, name := range []string{"broken", "panics"} {
		_, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`"}}`)
		res, ok := out["result"].(map[string]any)
		if !ok || res["isError"] != true {
			t.Fatalf("%s : %v", name, out)
		}
	}
	_, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"broken"}}`)
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "introuvable") {
		t.Errorf("texte de l'erreur = %q", text)
	}
}

func TestServer_UnknownToolAndMethod(t *testing.T) {
	_, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"absent"}}`)
	if out["error"] == nil {
		t.Errorf("outil inconnu : %v", out)
	}
	_, out = rpc(t, echoServer(), "", `{"jsonrpc":"2.0","id":5,"method":"server/discover"}`)
	if e, _ := out["error"].(map[string]any); e == nil || e["code"] != float64(-32601) {
		t.Errorf("méthode inconnue : %v", out)
	}
}

func TestServer_NotificationAndGet(t *testing.T) {
	code, out := rpc(t, echoServer(), "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusAccepted || out != nil {
		t.Errorf("notification = %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	echoServer().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", rec.Code)
	}
	code, _ = rpc(t, echoServer(), "", `pas du json`)
	if code != http.StatusBadRequest {
		t.Errorf("corps illisible = %d", code)
	}
}

// Le Hub ne sert une session qu'à qui présente son jeton, et plus du tout
// une fois fermée.
func TestHub_TokenGatesSession(t *testing.T) {
	h := NewHub()
	token, closeFn, err := h.Open(echoServer())
	if err != nil || len(token) < 40 {
		t.Fatalf("Open = %q, %v", token, err)
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if code, _ := rpc(t, h, "", list); code != http.StatusUnauthorized {
		t.Errorf("sans jeton = %d", code)
	}
	if code, _ := rpc(t, h, "mauvais", list); code != http.StatusUnauthorized {
		t.Errorf("mauvais jeton = %d", code)
	}
	if code, out := rpc(t, h, token, list); code != 200 || out["result"] == nil {
		t.Errorf("bon jeton = %d %v", code, out)
	}
	other, closeOther, _ := h.Open(Server{Name: "autre"})
	defer closeOther()
	if other == token {
		t.Error("deux sessions ont le même jeton")
	}
	closeFn()
	if code, _ := rpc(t, h, token, list); code != http.StatusUnauthorized {
		t.Errorf("session fermée = %d", code)
	}
}

func TestHub_ListenServesOnLoopback(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url, err := h.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q", url)
	}
	token, closeFn, _ := h.Open(echoServer())
	defer closeFn()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("statut = %d", resp.StatusCode)
	}
}
