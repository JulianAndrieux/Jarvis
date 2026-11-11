package claudeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

const (
	// DefaultModel : le modèle de l'assistant quand rien n'est précisé.
	DefaultModel = "claude-opus-5-5"
	// DefaultMaxTokens : plafond d'une réponse. Assez pour ne pas couper
	// une réponse au milieu, assez bas pour une requête sans flux.
	DefaultMaxTokens = 16000
	// DefaultTimeout : une requête qui réfléchit longtemps reste bornée.
	DefaultTimeout = 5 * time.Minute
)

// Config : les réglages du client.
type Config struct {
	// APIKey : vide, le SDK cherche ANTHROPIC_API_KEY puis ses profils.
	APIKey string
	// BaseURL : vide = l'API d'Anthropic (les tests y mettent un faux).
	BaseURL string
	Model   string // vide : DefaultModel
	// Effort : low, medium, high, xhigh, max ; vide = défaut du modèle.
	Effort    string
	MaxTokens int           // 0 : DefaultMaxTokens
	Timeout   time.Duration // 0 : DefaultTimeout
	// MaxRetries : 0 = défaut du SDK (2) ; négatif = aucun.
	MaxRetries int
}

// SDKClient : Client par le SDK officiel d'Anthropic.
type SDKClient struct {
	cfg    Config
	client anthropic.Client
}

func NewSDKClient(cfg Config) *SDKClient {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	opts := []option.RequestOption{option.WithRequestTimeout(cfg.Timeout)}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	switch {
	case cfg.MaxRetries < 0:
		opts = append(opts, option.WithMaxRetries(0))
	case cfg.MaxRetries > 0:
		opts = append(opts, option.WithMaxRetries(cfg.MaxRetries))
	}
	return &SDKClient{cfg: cfg, client: anthropic.NewClient(opts...)}
}

// Model : le modèle demandé (pour la provenance).
func (c *SDKClient) Model() string { return c.cfg.Model }

func (c *SDKClient) Complete(ctx context.Context, req Request) (Response, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.cfg.Model),
		MaxTokens: int64(c.cfg.MaxTokens),
		// Un refus des classifieurs de sécurité est resservi par un modèle
		// de repli dans le même appel (Response.Model dit lequel).
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		// Le préfixe (outils, consigne, conversation) est relu depuis le
		// cache d'un tour à l'autre.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
	}
	if c.cfg.Effort != "" {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(c.cfg.Effort)}
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	for _, t := range req.Tools {
		tool := anthropic.BetaToolParam{Name: t.Name, InputSchema: schemaParam(t.Schema)}
		if t.Description != "" {
			tool.Description = anthropic.String(t.Description)
		}
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &tool})
	}
	for i, m := range req.Messages {
		p, err := messageParam(m)
		if err != nil {
			return Response{}, fmt.Errorf("claudeapi: message %d : %w", i, err)
		}
		params.Messages = append(params.Messages, p)
	}

	msg, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return Response{}, classify(err)
	}
	raw, err := json.Marshal(msg.ToParam())
	if err != nil {
		return Response{}, fmt.Errorf("claudeapi: réponse non rejouable : %w", err)
	}
	resp := Response{
		Raw:   raw,
		Stop:  StopReason(msg.StopReason),
		Model: string(msg.Model),
		Usage: Usage{
			Input: int(msg.Usage.InputTokens), Output: int(msg.Usage.OutputTokens),
			CacheRead: int(msg.Usage.CacheReadInputTokens), CacheWrite: int(msg.Usage.CacheCreationInputTokens),
		},
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		resp.Refusal = string(msg.StopDetails.Category)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			text.WriteString(b.Text)
		case anthropic.BetaToolUseBlock:
			resp.Calls = append(resp.Calls, Call{ID: b.ID, Name: b.Name, Input: json.RawMessage(b.JSON.Input.Raw())})
		}
	}
	resp.Text = text.String()
	return resp, nil
}

// schemaParam : le JSON Schema d'un outil, au format du SDK (type "object"
// implicite, le reste recopié).
func schemaParam(schema map[string]any) anthropic.BetaToolInputSchemaParam {
	p := anthropic.BetaToolInputSchemaParam{}
	extra := map[string]any{}
	for k, v := range schema {
		switch k {
		case "type":
		case "properties":
			p.Properties = v
		case "required":
			switch r := v.(type) {
			case []string:
				p.Required = r
			case []any:
				for _, x := range r {
					if s, ok := x.(string); ok {
						p.Required = append(p.Required, s)
					}
				}
			}
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		p.ExtraFields = extra
	}
	return p
}

func messageParam(m Message) (anthropic.BetaMessageParam, error) {
	set := 0
	if m.User != "" {
		set++
	}
	if len(m.Assistant) > 0 {
		set++
	}
	if len(m.Results) > 0 {
		set++
	}
	if set != 1 {
		return anthropic.BetaMessageParam{}, errors.New("exactement un de User, Assistant, Results est attendu")
	}
	switch {
	case m.User != "":
		return anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(m.User)), nil
	case len(m.Assistant) > 0:
		var p anthropic.BetaAssistantMessageParam
		if err := json.Unmarshal(m.Assistant, &p); err != nil {
			return anthropic.BetaMessageParam{}, fmt.Errorf("message du modèle illisible : %w", err)
		}
		return anthropic.BetaMessageParam{OfAssistant: &p}, nil
	default:
		blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(m.Results))
		for _, r := range m.Results {
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		return anthropic.NewBetaUserMessage(blocks...), nil
	}
}

// classify range une erreur de l'API dans une des erreurs du port.
func classify(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("claudeapi: %w", err)
	}
	switch code := apiErr.StatusCode; {
	case code == 401 || code == 403:
		return fmt.Errorf("%w (HTTP %d) : %v", ErrAuth, code, err)
	case code == 429:
		return fmt.Errorf("%w (HTTP %d) : %v", ErrRateLimited, code, err)
	case code >= 500:
		return fmt.Errorf("%w (HTTP %d) : %v", ErrOverloaded, code, err)
	default:
		return fmt.Errorf("claudeapi: requête refusée (HTTP %d) : %w", code, err)
	}
}
