package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClient_Extract_Success(t *testing.T) {
	var gotReq chatCompletionRequest
	var gotPath, gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{
				{Message: chatMessage{Content: `{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}`}},
			},
		})
	}))
	defer srv.Close()

	c := HTTPClient{
		BaseURL:      srv.URL + "/v1",
		Model:        "qwen3-8b",
		ModelVersion: "Q5_K_M-2026-01",
	}
	req := ExtractRequest{
		Page:   1,
		Text:   "Facture F-1, Acme, 123.45 EUR",
		Schema: json.RawMessage(`{"type":"object","properties":{"numero":{"type":"string"}}}`),
		Prompt: "Extrait les champs suivants",
	}

	got, err := c.Extract(context.Background(), req)
	if err != nil {
		t.Fatalf("Extract() error = %v, want nil", err)
	}

	if string(got.JSON) != `{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}` {
		t.Errorf("JSON = %s, want the server's content", got.JSON)
	}
	if got.Model != (ModelInfo{Name: "qwen3-8b", Version: "Q5_K_M-2026-01"}) {
		t.Errorf("Model = %+v, want the client's configured model info", got.Model)
	}
	if got.Prompt != req.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, req.Prompt)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", gotPath)
	}
	if gotReq.Model != "qwen3-8b" {
		t.Errorf("request model = %q, want qwen3-8b", gotReq.Model)
	}
	if gotReq.ResponseFormat == nil {
		t.Fatal("request response_format is nil, want it set to constrain decoding by the schema")
	}
	if gotReq.ResponseFormat.Type != "json_schema" {
		t.Errorf("response_format.type = %q, want json_schema", gotReq.ResponseFormat.Type)
	}
	if string(gotReq.ResponseFormat.JSONSchema.Schema) != string(req.Schema) {
		t.Errorf("response_format.json_schema.schema = %s, want %s", gotReq.ResponseFormat.JSONSchema.Schema, req.Schema)
	}
	if len(gotReq.Messages) != 1 {
		t.Fatalf("messages = %+v, want 1 message", gotReq.Messages)
	}
	if !strings.Contains(gotReq.Messages[0].Content, req.Prompt) {
		t.Errorf("message content = %q, want it to contain the prompt %q", gotReq.Messages[0].Content, req.Prompt)
	}
	if !strings.Contains(gotReq.Messages[0].Content, req.Text) {
		t.Errorf("message content = %q, want it to contain the source text %q", gotReq.Messages[0].Content, req.Text)
	}
}

func TestHTTPClient_Extract_ServerErrorStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("model not loaded"))
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.Extract(context.Background(), ExtractRequest{Page: 1, Text: "x", Schema: json.RawMessage(`{}`)})

	if err == nil {
		t.Fatal("Extract() error = nil, want non-nil for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("Extract() error = %v, want it to mention the status and body", err)
	}
}

func TestHTTPClient_Extract_MalformedResponseEnvelope_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.Extract(context.Background(), ExtractRequest{Page: 1, Text: "x", Schema: json.RawMessage(`{}`)})

	if err == nil {
		t.Fatal("Extract() error = nil, want non-nil for a malformed JSON envelope")
	}
}

func TestHTTPClient_Extract_ContentIsNotValidJSON_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{{Message: chatMessage{Content: "this is not json"}}},
		})
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.Extract(context.Background(), ExtractRequest{Page: 1, Text: "x", Schema: json.RawMessage(`{}`)})

	if err == nil {
		t.Fatal("Extract() error = nil, want non-nil when the model's content is not valid JSON")
	}
}

func TestHTTPClient_Extract_NoChoices_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{Choices: nil})
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.Extract(context.Background(), ExtractRequest{Page: 1, Text: "x", Schema: json.RawMessage(`{}`)})

	if err == nil {
		t.Fatal("Extract() error = nil, want non-nil when the server returns no choices")
	}
}

func TestHTTPClient_Extract_CanceledContext_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{{Message: chatMessage{Content: `{}`}}},
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.Extract(ctx, ExtractRequest{Page: 1, Text: "x", Schema: json.RawMessage(`{}`)})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Extract() error = %v, want it to wrap context.Canceled", err)
	}
}

func TestHTTPClient_Extract_TrailingSlashInBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{{Message: chatMessage{Content: `{}`}}},
		})
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL + "/", Model: "m"}
	if _, err := c.Extract(context.Background(), ExtractRequest{Page: 1, Text: "x", Schema: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Extract() error = %v, want nil", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions (no double slash)", gotPath)
	}
}
