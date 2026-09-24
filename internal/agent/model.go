package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPModel implémente Model contre une API chat completions compatible
// OpenAI avec appels d'outils — llama.cpp server (vérifié empiriquement
// avec Qwen3-8B avant d'écrire ce code : un appel read_file correct en
// ~4 s).
type HTTPModel struct {
	BaseURL string // ex. "http://127.0.0.1:8081/v1"
	Model   string
	HTTP    *http.Client // nil : http.DefaultClient
	// Temperature : 0 par défaut (déterministe).
	Temperature float64
	// MaxTokens borne chaque réponse (0 : pas de limite envoyée). Vu en
	// réel : sans limite, une réécriture complète qui s'emballe a généré
	// 6 minutes jusqu'à saturer le contexte.
	MaxTokens int
}

// WithTemperature retourne une copie du client à la température donnée
// (voir Developer : une relance à température 0 rejoue le même déroulé).
func (m HTTPModel) WithTemperature(t float64) Model {
	m.Temperature = t
	return m
}

type wireToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func (m HTTPModel) Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Message, error) {
	req := struct {
		Model       string        `json:"model"`
		Messages    []wireMessage `json:"messages"`
		Tools       []wireTool    `json:"tools,omitempty"`
		Temperature float64       `json:"temperature"`
		MaxTokens   int           `json:"max_tokens,omitempty"`
	}{Model: m.Model, Temperature: m.Temperature, MaxTokens: m.MaxTokens}
	for _, msg := range msgs {
		w := wireMessage{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID}
		for _, c := range msg.ToolCalls {
			wc := wireToolCall{ID: c.ID, Type: "function"}
			wc.Function.Name, wc.Function.Arguments = c.Name, c.Arguments
			w.ToolCalls = append(w.ToolCalls, wc)
		}
		req.Messages = append(req.Messages, w)
	}
	for _, spec := range tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name, wt.Function.Description, wt.Function.Parameters = spec.Name, spec.Description, spec.Parameters
		req.Tools = append(req.Tools, wt)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, fmt.Errorf("agent: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, fmt.Errorf("agent: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := m.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("agent: request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, fmt.Errorf("agent: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Message{}, fmt.Errorf("agent: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message wireMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Message{}, fmt.Errorf("agent: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return Message{}, fmt.Errorf("agent: server returned no choices")
	}
	w := parsed.Choices[0].Message
	out := Message{Role: "assistant", Content: w.Content}
	for _, c := range w.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out, nil
}
