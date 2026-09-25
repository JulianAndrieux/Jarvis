// Package claudecode fait travailler Claude Code (le CLI `claude`, sans
// interface) sur les tickets de Jarvis (jalon 41) : analyse, développement
// et relecture, à la place du modèle local quand le ticket le demande.
// Chaque action de Claude (fichier lu, commande lancée) rejoint le fil du
// ticket, comme pour l'agent local.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Request : une session de Claude Code.
type Request struct {
	// Dir : le dossier de travail (le dépôt, ou la copie de travail du
	// ticket) — Claude y lit son CLAUDE.md.
	Dir    string
	Prompt string
	// System : consignes ajoutées au prompt système de Claude Code.
	System string
	// Tools : les seuls outils autorisés sans demander (un outil absent est
	// refusé : personne ne répond aux demandes en mode non interactif).
	Tools []string
	// Edit : modifications de fichiers acceptées (dans Dir).
	Edit bool
	// Schema : la réponse finale suit ce schéma JSON (Result.Structured).
	Schema json.RawMessage
}

// Result : la fin d'une session.
type Result struct {
	Text       string
	Structured json.RawMessage
	CostUSD    float64
	Turns      int
}

// Runner lance une session — CLI en production, une fake dans les tests.
type Runner interface {
	Run(ctx context.Context, req Request, onStep func(tickets.AgentStep)) (Result, error)
}

// CLI lance le binaire `claude` en mode non interactif.
type CLI struct {
	Binary string // "" : claude, cherché sur le PATH
	Model  string // "" : le modèle par défaut de Claude Code
	// Timeout : durée maximale d'une session (0 : 1 h).
	Timeout time.Duration
}

func (c CLI) args(req Request) []string {
	mode := "default"
	if req.Edit {
		mode = "acceptEdits"
	}
	args := []string{
		"-p", req.Prompt,
		"--output-format", "stream-json", "--verbose",
		"--no-session-persistence",
		// Les serveurs MCP de l'utilisateur (Gmail, Drive…) n'ont rien à
		// faire dans un ticket.
		"--strict-mcp-config",
		"--permission-mode", mode,
		"--allowedTools", strings.Join(req.Tools, ","),
	}
	if req.System != "" {
		args = append(args, "--append-system-prompt", req.System)
	}
	if len(req.Schema) > 0 {
		args = append(args, "--json-schema", string(req.Schema))
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return args
}

// Run lance la session et suit son flux jusqu'au résultat.
func (c CLI) Run(ctx context.Context, req Request, onStep func(tickets.AgentStep)) (Result, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bin := c.Binary
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, c.args(req)...)
	cmd.Dir = req.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("claude code : %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("claude code : lancement impossible (%s) : %w", bin, err)
	}
	res, perr := parse(stdout, req.Dir, onStep)
	io.Copy(io.Discard, stdout)
	werr := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, fmt.Errorf("claude code : session arrêtée après %s", timeout)
	}
	if perr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Result{}, fmt.Errorf("%w — %s", perr, clip(msg, 2000))
		}
		return Result{}, perr
	}
	if werr != nil {
		return Result{}, fmt.Errorf("claude code : %w — %s", werr, clip(strings.TrimSpace(stderr.String()), 2000))
	}
	return res, nil
}

// event : une ligne du flux stream-json (seuls les champs utiles).
type event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// Message : un objet ({content}) pour assistant/user, un texte pour
	// un refus de permission.
	Message    json.RawMessage `json:"message"`
	ToolName   string          `json:"tool_name"`
	IsError    bool            `json:"is_error"`
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured_output"`
	Cost       float64         `json:"total_cost_usd"`
	Turns      int             `json:"num_turns"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// parse suit le flux : un texte de Claude devient une étape, un appel
// d'outil aussi, une fois son résultat connu (en détail).
func parse(r io.Reader, dir string, onStep func(tickets.AgentStep)) (Result, error) {
	br := bufio.NewReader(r)
	pending := map[string]string{} // appel d'outil -> son résumé
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e event
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				return Result{}, fmt.Errorf("claude code : sortie inattendue : %s", clip(strings.TrimSpace(string(line)), 500))
			}
			switch e.Type {
			case "system":
				if e.Subtype == "permission_denied" {
					var msg string
					json.Unmarshal(e.Message, &msg)
					onStep(tickets.AgentStep{Summary: "⚠ Refusé : " + e.ToolName, Detail: msg})
				}
			case "assistant", "user":
				var msg struct {
					Content json.RawMessage `json:"content"`
				}
				json.Unmarshal(e.Message, &msg)
				var blocks []block
				json.Unmarshal(msg.Content, &blocks) // un texte seul : rien à suivre
				for _, b := range blocks {
					switch b.Type {
					case "text":
						if text := strings.TrimSpace(b.Text); text != "" {
							first, _, _ := strings.Cut(text, "\n")
							onStep(tickets.AgentStep{Summary: "Claude : " + clip(first, 200), Detail: text})
						}
					case "tool_use":
						if b.Name != "StructuredOutput" {
							pending[b.ID] = toolSummary(b.Name, b.Input, dir)
						}
					case "tool_result":
						summary, ok := pending[b.ToolUseID]
						if !ok {
							continue
						}
						delete(pending, b.ToolUseID)
						if b.IsError {
							summary = "⚠ " + summary
						}
						onStep(tickets.AgentStep{Summary: summary, Detail: resultText(b.Content)})
					}
				}
			case "result":
				if e.IsError || e.Subtype != "success" {
					return Result{}, fmt.Errorf("claude code : session en échec (%s) %s", e.Subtype, clip(e.Result, 1000))
				}
				return Result{Text: e.Result, Structured: e.Structured, CostUSD: e.Cost, Turns: e.Turns}, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return Result{}, errors.New("claude code : session terminée sans résultat")
			}
			return Result{}, fmt.Errorf("claude code : lecture : %w", err)
		}
	}
}

// toolSummary : l'appel d'outil en une ligne (chemins relatifs à dir).
func toolSummary(name string, input json.RawMessage, dir string) string {
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		Command  string `json:"command"`
	}
	json.Unmarshal(input, &in)
	// Claude peut écrire le chemin résolu du dossier (macOS : /var ->
	// /private/var) : relatif à l'un ou à l'autre.
	roots := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		roots = append(roots, resolved)
	}
	rel := func(p string) string {
		for _, root := range roots {
			if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
				return r
			}
		}
		return p
	}
	switch {
	case in.Command != "":
		first, _, _ := strings.Cut(strings.TrimSpace(in.Command), "\n")
		return name + " : " + clip(first, 200)
	case in.FilePath != "":
		return name + " " + rel(in.FilePath)
	case in.Pattern != "":
		s := name + " " + in.Pattern
		if in.Path != "" {
			s += " dans " + rel(in.Path)
		}
		return s
	}
	return name
}

// resultText : le contenu d'un résultat d'outil (texte, ou liste de blocs).
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
