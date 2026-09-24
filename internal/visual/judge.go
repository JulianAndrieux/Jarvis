package visual

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Capture : une page, avant (version en service) et après (version du
// ticket), en PNG.
type Capture struct {
	Page   string
	Before []byte
	After  []byte
}

// JudgeRequest : le besoin du ticket et les captures à juger.
type JudgeRequest struct {
	Title, Need, Acceptance string
	Captures                []Capture
}

// VisionJudge interroge un modèle de vision servi par llama.cpp (API
// compatible OpenAI, images en data URI) — Devstral Small 2 avec son
// module de vision, déjà chargé pour les tickets.
type VisionJudge struct {
	BaseURL   string // ex. http://127.0.0.1:8082/v1
	Model     string
	HTTP      *http.Client // nil : http.DefaultClient
	MaxTokens int          // 0 : 2048
}

const judgePrompt = `Tu relis visuellement un changement d'interface de l'application Jarvis. Pour chaque page, tu reçois deux captures : AVANT (version en service) puis APRÈS (version du ticket).
Réponds à deux questions, en regardant réellement les captures :
1. La capture APRÈS montre-t-elle exactement ce que demande le ticket (position, alignement, libellés, éléments visibles) ? Un besoin rempli à moitié est « a_reprendre ».
2. Quelque chose d'autre a-t-il changé ou cassé par rapport à AVANT (élément disparu, chevauchement, texte coupé, mise en page déformée) ?
Décris précisément où se trouvent les éléments concernés. Ne signale que ce que tu vois. La liste des problèmes ne contient QUE des problèmes à corriger (jamais un constat positif) ; si tout va bien, elle est vide. Réponds uniquement en JSON : verdict « acceptable » ou « a_reprendre », un résumé, et les problèmes (page, gravité bloquant/important/mineur, message avec la correction attendue).`

// schema : la forme imposée de la réponse (décodage contraint).
var schema = map[string]any{
	"type": "object", "required": []string{"verdict", "summary", "issues"},
	"properties": map[string]any{
		"verdict": map[string]any{"type": "string", "enum": []string{"acceptable", "a_reprendre"}},
		"summary": map[string]any{"type": "string"},
		"issues": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "required": []string{"page", "severity", "message"},
			"properties": map[string]any{
				"page":     map[string]any{"type": "string"},
				"severity": map[string]any{"type": "string", "enum": []string{"bloquant", "important", "mineur"}},
				"message":  map[string]any{"type": "string"},
			},
		}},
	},
}

type judgeVerdict struct {
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
	Issues  []struct {
		Page, Severity, Message string
	} `json:"issues"`
}

// Judge fait juger les captures.
func (j VisionJudge) Judge(ctx context.Context, req JudgeRequest) (tickets.ReviewResult, error) {
	content := []map[string]any{{"type": "text", "text": fmt.Sprintf("%s\n\nTicket : %s\nBesoin : %s\nCritères d'acceptation : %s", judgePrompt, req.Title, req.Need, req.Acceptance)}}
	for _, c := range req.Captures {
		content = append(content,
			map[string]any{"type": "text", "text": "Page " + c.Page + " — AVANT :"},
			image(c.Before),
			map[string]any{"type": "text", "text": "Page " + c.Page + " — APRÈS :"},
			image(c.After),
		)
	}
	maxTokens := j.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	body, err := json.Marshal(map[string]any{
		"model":       j.Model,
		"messages":    []any{map[string]any{"role": "user", "content": content}},
		"temperature": 0,
		"max_tokens":  maxTokens,
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "relecture_visuelle", "schema": schema,
		}},
	})
	if err != nil {
		return tickets.ReviewResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(j.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return tickets.ReviewResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := j.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return tickets.ReviewResult{}, fmt.Errorf("visual: modèle de vision : %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tickets.ReviewResult{}, fmt.Errorf("visual: modèle de vision : %s : %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return tickets.ReviewResult{}, fmt.Errorf("visual: réponse illisible : %s", strings.TrimSpace(string(raw)))
	}
	var v judgeVerdict
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &v); err != nil || (v.Verdict != "acceptable" && v.Verdict != "a_reprendre") {
		return tickets.ReviewResult{}, fmt.Errorf("visual: verdict illisible : %q", out.Choices[0].Message.Content)
	}
	res := tickets.ReviewResult{Approved: v.Verdict == "acceptable", Summary: strings.TrimSpace(v.Summary)}
	for _, i := range v.Issues {
		res.Issues = append(res.Issues, tickets.ReviewIssue{File: "capture " + i.Page, Severity: i.Severity, Message: i.Message})
		if i.Severity == "bloquant" {
			res.Approved = false
		}
	}
	return res, nil
}

func image(png []byte) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}}
}
