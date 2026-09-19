package vlm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClient_ParsePage_Success(t *testing.T) {
	var gotReq chatCompletionRequest
	var gotPath, gotMethod, gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{
				{Message: chatCompletionMessage{Content: "# Facture\n\nTotal: 123.45 EUR"}},
			},
		})
	}))
	defer srv.Close()

	c := HTTPClient{
		BaseURL:      srv.URL + "/v1",
		Model:        "olmOCR-2-7B-1025",
		ModelVersion: "Q4_K_M-2026-01",
		Prompt:       "extract layout as markdown",
	}

	png := []byte("fake-png-bytes")
	got, err := c.ParsePage(context.Background(), PageImage{Page: 1, PNG: png})
	if err != nil {
		t.Fatalf("ParsePage() error = %v, want nil", err)
	}

	if got.Markdown != "# Facture\n\nTotal: 123.45 EUR" {
		t.Errorf("Markdown = %q, want the server's content", got.Markdown)
	}
	if got.Model != (ModelInfo{Name: "olmOCR-2-7B-1025", Version: "Q4_K_M-2026-01"}) {
		t.Errorf("Model = %+v, want the client's configured model info", got.Model)
	}
	if got.Prompt != "extract layout as markdown" {
		t.Errorf("Prompt = %q, want the configured prompt", got.Prompt)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotReq.Model != "olmOCR-2-7B-1025" {
		t.Errorf("request model = %q, want olmOCR-2-7B-1025", gotReq.Model)
	}
	if len(gotReq.Messages) != 1 || len(gotReq.Messages[0].Content) != 2 {
		t.Fatalf("request messages = %+v, want 1 message with 2 content parts", gotReq.Messages)
	}
	wantDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if gotReq.Messages[0].Content[1].ImageURL == nil || gotReq.Messages[0].Content[1].ImageURL.URL != wantDataURI {
		t.Errorf("image content = %+v, want a data URI with the base64-encoded PNG", gotReq.Messages[0].Content[1])
	}
	if gotReq.Messages[0].Content[0].Text != "extract layout as markdown" {
		t.Errorf("text content = %q, want the configured prompt", gotReq.Messages[0].Content[0].Text)
	}
}

func TestHTTPClient_ParsePage_UsesDefaultPromptWhenUnset(t *testing.T) {
	var gotReq chatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{{Message: chatCompletionMessage{Content: "ok"}}},
		})
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	if _, err := c.ParsePage(context.Background(), PageImage{Page: 1, PNG: []byte("x")}); err != nil {
		t.Fatalf("ParsePage() error = %v, want nil", err)
	}

	if gotReq.Messages[0].Content[0].Text != DefaultPrompt {
		t.Errorf("text content = %q, want DefaultPrompt", gotReq.Messages[0].Content[0].Text)
	}
}

func TestHTTPClient_ParsePage_ServerErrorStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("model not loaded"))
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.ParsePage(context.Background(), PageImage{Page: 1, PNG: []byte("x")})

	if err == nil {
		t.Fatal("ParsePage() error = nil, want non-nil for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("ParsePage() error = %v, want it to mention the status and body", err)
	}
}

func TestHTTPClient_ParsePage_MalformedJSON_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.ParsePage(context.Background(), PageImage{Page: 1, PNG: []byte("x")})

	if err == nil {
		t.Fatal("ParsePage() error = nil, want non-nil for a malformed JSON response")
	}
}

func TestHTTPClient_ParsePage_NoChoices_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{Choices: nil})
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.ParsePage(context.Background(), PageImage{Page: 1, PNG: []byte("x")})

	if err == nil {
		t.Fatal("ParsePage() error = nil, want non-nil when the server returns no choices")
	}
}

func TestHTTPClient_ParsePage_CanceledContext_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{{Message: chatCompletionMessage{Content: "ok"}}},
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := HTTPClient{BaseURL: srv.URL, Model: "m"}
	_, err := c.ParsePage(ctx, PageImage{Page: 1, PNG: []byte("x")})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParsePage() error = %v, want it to wrap context.Canceled", err)
	}
}

func TestHTTPClient_ParsePage_TrailingSlashInBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []chatCompletionChoice{{Message: chatCompletionMessage{Content: "ok"}}},
		})
	}))
	defer srv.Close()

	c := HTTPClient{BaseURL: srv.URL + "/", Model: "m"}
	if _, err := c.ParsePage(context.Background(), PageImage{Page: 1, PNG: []byte("x")}); err != nil {
		t.Fatalf("ParsePage() error = %v, want nil", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions (no double slash)", gotPath)
	}
}
