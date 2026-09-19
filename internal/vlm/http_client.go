package vlm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultPrompt est l'instruction envoyée au VLM quand HTTPClient.Prompt
// n'est pas renseigné. Elle demande explicitement une transcription
// fidèle au layout, sans résumé — conforme à l'étage Parsing du brief.
const DefaultPrompt = "Transcris fidèlement le contenu de cette page en Markdown, en respectant la mise en page (titres, tableaux, listes). Ne résume pas, ne commente pas, renvoie uniquement le Markdown."

// HTTPClient implémente Client contre un serveur exposant une API
// compatible OpenAI (chat completions), typiquement llama.cpp server
// servant un VLM de document en local.
type HTTPClient struct {
	// BaseURL est l'URL de base de l'API, ex. "http://localhost:8080/v1".
	// Un "/" final est toléré.
	BaseURL string
	// Model est l'identifiant de modèle envoyé au serveur, et journalisé
	// tel quel dans ModelInfo.Name pour la reproductibilité.
	Model string
	// ModelVersion est journalisé dans ModelInfo.Version (ex. le tag de
	// quantization GGUF). N'est jamais envoyé au serveur.
	ModelVersion string
	// Prompt est l'instruction envoyée avec chaque image de page. Une
	// valeur vide retombe sur DefaultPrompt.
	Prompt string
	// HTTP est le client HTTP utilisé ; nil retombe sur http.DefaultClient.
	HTTP *http.Client
}

func (c HTTPClient) ParsePage(ctx context.Context, img PageImage) (ParseResult, error) {
	prompt := c.Prompt
	if prompt == "" {
		prompt = DefaultPrompt
	}

	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG)
	reqBody := chatCompletionRequest{
		Model: c.Model,
		Messages: []chatCompletionRequestMessage{
			{
				Role: "user",
				Content: []chatCompletionContentPart{
					{Type: "text", Text: prompt},
					{Type: "image_url", ImageURL: &chatCompletionImageURL{URL: dataURI}},
				},
			},
		},
		Temperature: 0,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ParseResult{}, fmt.Errorf("vlm: marshal request for page %d: %w", img.Page, err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ParseResult{}, fmt.Errorf("vlm: build request for page %d: %w", img.Page, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ParseResult{}, fmt.Errorf("vlm: request page %d: %w", img.Page, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ParseResult{}, fmt.Errorf("vlm: read response for page %d: %w", img.Page, err)
	}

	if resp.StatusCode != http.StatusOK {
		return ParseResult{}, fmt.Errorf("vlm: server returned %d for page %d: %s", resp.StatusCode, img.Page, strings.TrimSpace(string(respBytes)))
	}

	var respBody chatCompletionResponse
	if err := json.Unmarshal(respBytes, &respBody); err != nil {
		return ParseResult{}, fmt.Errorf("vlm: decode response for page %d: %w", img.Page, err)
	}

	if len(respBody.Choices) == 0 {
		return ParseResult{}, fmt.Errorf("vlm: server returned no choices for page %d", img.Page)
	}

	return ParseResult{
		Markdown: respBody.Choices[0].Message.Content,
		Model:    ModelInfo{Name: c.Model, Version: c.ModelVersion},
		Prompt:   prompt,
	}, nil
}

// Sous-ensemble du format de requête/réponse "chat completions" compatible
// OpenAI, tel qu'exposé par llama.cpp server. On ne modélise que ce que
// jarvis utilise réellement.

type chatCompletionRequest struct {
	Model       string                         `json:"model"`
	Messages    []chatCompletionRequestMessage `json:"messages"`
	Temperature float64                        `json:"temperature"`
}

// chatCompletionRequestMessage est un message de requête : son Content est
// une liste de parts (texte + image), format "multimodal" de l'API chat
// completions compatible OpenAI.
type chatCompletionRequestMessage struct {
	Role    string                      `json:"role"`
	Content []chatCompletionContentPart `json:"content"`
}

type chatCompletionContentPart struct {
	Type     string                  `json:"type"`
	Text     string                  `json:"text,omitempty"`
	ImageURL *chatCompletionImageURL `json:"image_url,omitempty"`
}

type chatCompletionImageURL struct {
	URL string `json:"url"`
}

type chatCompletionResponse struct {
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Message chatCompletionMessage `json:"message"`
}

// chatCompletionMessage est un message de réponse : son Content est une
// simple chaîne, conformément au format des réponses chat completions.
type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
