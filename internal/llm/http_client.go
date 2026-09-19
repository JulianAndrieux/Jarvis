package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPClient implémente Client contre un serveur exposant une API
// compatible OpenAI (chat completions) avec décodage contraint par JSON
// Schema (`response_format: {"type": "json_schema", ...}`), tel qu'exposé
// par llama.cpp server. Le schéma est celui dérivé par internal/schema :
// il garantit la conformité structurelle de la réponse, mais pas la
// justesse des valeurs — c'est le rôle du score de confiance par champ.
type HTTPClient struct {
	// BaseURL est l'URL de base de l'API, ex. "http://localhost:8081/v1".
	// Un "/" final est toléré.
	BaseURL string
	// Model est l'identifiant de modèle envoyé au serveur, et journalisé
	// tel quel dans ModelInfo.Name pour la reproductibilité.
	Model string
	// ModelVersion est journalisé dans ModelInfo.Version (ex. le tag de
	// quantization GGUF). N'est jamais envoyé au serveur.
	ModelVersion string
	// HTTP est le client HTTP utilisé ; nil retombe sur http.DefaultClient.
	HTTP *http.Client
	// DisableThinking ajoute "/no_think" au message envoyé — convention du
	// template de chat Qwen3 pour désactiver le mode "réflexion", qui peut
	// sinon générer un nombre de tokens très élevé et variable avant de
	// conclure (observé : 2614 tokens/~200s sur un cas ambigu contre 156
	// tokens/~10s avec /no_think, même réponse correcte — voir CLAUDE.md).
	// Sans effet connu sur d'autres familles de modèles.
	DisableThinking bool
}

func (c HTTPClient) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	reqBody := chatCompletionRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "user", Content: buildExtractionMessage(req, c.DisableThinking)},
		},
		Temperature: 0,
		ResponseFormat: &responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchemaSpec{
				Name:   "extraction",
				Schema: req.Schema,
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("llm: marshal request for page %d: %w", req.Page, err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("llm: build request for page %d: %w", req.Page, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("llm: request page %d: %w", req.Page, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("llm: read response for page %d: %w", req.Page, err)
	}

	if resp.StatusCode != http.StatusOK {
		return ExtractResult{}, fmt.Errorf("llm: server returned %d for page %d: %s", resp.StatusCode, req.Page, strings.TrimSpace(string(respBytes)))
	}

	var respBody chatCompletionResponse
	if err := json.Unmarshal(respBytes, &respBody); err != nil {
		return ExtractResult{}, fmt.Errorf("llm: decode response envelope for page %d: %w", req.Page, err)
	}

	if len(respBody.Choices) == 0 {
		return ExtractResult{}, fmt.Errorf("llm: server returned no choices for page %d", req.Page)
	}

	content := respBody.Choices[0].Message.Content
	if !json.Valid([]byte(content)) {
		return ExtractResult{}, fmt.Errorf("llm: model output for page %d is not valid JSON despite response_format=json_schema: %s", req.Page, content)
	}

	return ExtractResult{
		JSON:   json.RawMessage(content),
		Model:  ModelInfo{Name: c.Model, Version: c.ModelVersion},
		Prompt: req.Prompt,
	}, nil
}

func buildExtractionMessage(req ExtractRequest, disableThinking bool) string {
	msg := fmt.Sprintf("%s\n\n---\nTexte source (page %d) :\n%s", req.Prompt, req.Page, req.Text)
	if disableThinking {
		msg += "\n\n/no_think"
	}
	return msg
}

// Sous-ensemble du format de requête/réponse "chat completions" compatible
// OpenAI, étendu par response_format=json_schema (extension répandue,
// notamment exposée par llama.cpp server, pour le décodage contraint).

type chatCompletionRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string         `json:"type"`
	JSONSchema jsonSchemaSpec `json:"json_schema"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

type chatCompletionResponse struct {
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Message chatMessage `json:"message"`
}
