package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/mcp"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// toolServer : le nom du serveur d'outils de Jarvis vu par Claude (ses
// outils s'appellent mcp__jarvis__<nom>).
const toolServer = "jarvis"

// Assistant fait répondre Claude pour l'assistant de Jarvis (jalon 55,
// assistant.Runner) : aucun outil intégré — ni fichier, ni terminal, ni
// web —, seulement les outils de Jarvis, servis par Hub le temps de la
// réponse derrière un jeton à usage unique.
type Assistant struct {
	Runner Runner
	Hub    *mcp.Hub
	// URL : l'adresse du Hub (mcp.Hub.Listen).
	URL string
}

func (a Assistant) Run(ctx context.Context, req assistant.Request) (assistant.Reply, error) {
	if a.Hub == nil || a.URL == "" {
		return assistant.Reply{}, errors.New("claude code : serveur d'outils non démarré")
	}
	token, closeHub, err := a.Hub.Open(mcp.Server{Name: toolServer, Tools: req.Tools})
	if err != nil {
		return assistant.Reply{}, err
	}
	defer closeHub()
	// Un dossier vide : Claude n'y trouve ni CLAUDE.md ni fichier, et la
	// configuration (qui porte le jeton) n'est lisible que par nous.
	dir, err := os.MkdirTemp("", "jarvis-assistant-")
	if err != nil {
		return assistant.Reply{}, fmt.Errorf("claude code : dossier de session : %w", err)
	}
	defer os.RemoveAll(dir)
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{toolServer: map[string]any{
		"type": "http", "url": a.URL, "headers": map[string]string{"Authorization": "Bearer " + token},
	}}})
	if err != nil {
		return assistant.Reply{}, err
	}
	configPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return assistant.Reply{}, fmt.Errorf("claude code : configuration des outils : %w", err)
	}
	res, err := a.Runner.Run(ctx, Request{
		Dir: dir, Prompt: req.Prompt, PromptOnStdin: true,
		SystemPrompt: req.System, NoBuiltins: true, MCPConfig: configPath,
		Tools: []string{"mcp__" + toolServer + "__*"},
	}, func(tickets.AgentStep) {}) // les outils de Jarvis notent eux-mêmes ce qu'ils font
	if err != nil {
		return assistant.Reply{}, err
	}
	return assistant.Reply{Text: res.Text, CostUSD: res.CostUSD, Turns: res.Turns}, nil
}
