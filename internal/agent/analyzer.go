package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Message est un message de la conversation avec le modèle (rôles
// "system", "user", "assistant", "tool").
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // pour un message "tool" : l'appel auquel il répond
}

// Model est le port vers le modèle de l'agent — HTTPModel (llama.cpp,
// API compatible OpenAI avec appels d'outils) en production.
type Model interface {
	Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Message, error)
}

// Analyzer analyse un ticket en lecture seule et propose un plan
// (tickets.Analyst, jalon 27).
type Analyzer struct {
	Model Model
	Tools Tools
	// ProjectBrief : contexte du projet donné au modèle (contraintes,
	// architecture — le début de CLAUDE.md).
	ProjectBrief string
	// MaxSteps borne les appels d'outils (0 : 16) ; ContextChars la taille
	// de la conversation envoyée au modèle (0 : 18000 caractères, ~6000
	// jetons : le serveur actuel a 8192 jetons de contexte) ;
	// ToolOutputChars une sortie d'outil (0 : 3000).
	MaxSteps        int
	ContextChars    int
	ToolOutputChars int
	// DisableThinking ajoute "/no_think" (Qwen3 : sans lui, le modèle
	// peut générer des milliers de jetons de réflexion — cf. jalon 11).
	DisableThinking bool
	// Instructions, si renseigné, donne les consignes en vigueur, relues
	// à chaque analyse (agents.Registry) ; vide : DefaultAnalysisPrompt.
	Instructions func() string
}

// minPlanChars : en dessous, une réponse en texte libre n'est pas un plan.
const minPlanChars = 40

const compactedNote = "[sortie déjà lue, retirée pour tenir dans le contexte — relis le fichier si besoin]"

// DefaultAnalysisPrompt : consignes par défaut de l'agent d'analyse. Modifiables
// depuis l'interface (onglet Agents) ; le contexte du projet et
// /no_think sont toujours ajoutés par le code.
const DefaultAnalysisPrompt = `Tu es l'agent de développement de l'application Jarvis. Ta mission maintenant : ANALYSER un ticket, en lecture seule — tu ne modifies rien.

Explore le code avec les outils list_files, search et read_file. Lis ce qui est nécessaire, pas tout : commence par chercher les noms utiles, puis lis les passages concernés.
Quand tu as compris, appelle propose_plan avec un plan en Markdown, en français, avec exactement ces sections :
## Compréhension
## Fichiers concernés (uniquement des chemins que tu as vus)
## Tests à écrire d'abord (le projet est en TDD strict)
## Étapes
## Risques et questions
N'invente jamais un fichier, un type ou une fonction que tu n'as pas lu. Si le ticket est trop gros ou ambigu, dis-le dans « Risques et questions » plutôt que de deviner.

Conventions du code : les identifiants (types, fonctions, noms de fichiers) sont en anglais, les commentaires et les textes affichés en français. Cherche donc des identifiants anglais (Document, List, Row, Page...) plutôt que des mots français. Les pages web sont des gabarits templ (fichiers .templ ; les fichiers _templ.go sont générés, ne les lis pas). Si un outil répond ERREUR, lis le message : il propose souvent le bon chemin.
`

func (a *Analyzer) instructions() string {
	if a.Instructions != nil {
		if s := strings.TrimSpace(a.Instructions()); s != "" {
			return s + "\n"
		}
	}
	return DefaultAnalysisPrompt
}

func (a *Analyzer) systemPrompt() string {
	var b strings.Builder
	b.WriteString(a.instructions())
	if a.ProjectBrief != "" {
		b.WriteString("\nContexte du projet :\n")
		b.WriteString(a.ProjectBrief)
		b.WriteString("\n")
	}
	if a.DisableThinking {
		b.WriteString("\n/no_think\n")
	}
	return b.String()
}

func userPrompt(req tickets.AnalysisRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket : %s\n\nBesoin :\n%s\n", req.Title, req.Need)
	if strings.TrimSpace(req.Acceptance) != "" {
		fmt.Fprintf(&b, "\nCritères d'acceptation :\n%s\n", req.Acceptance)
	}
	if req.PreviousPlan != "" {
		fmt.Fprintf(&b, "\nTon plan précédent :\n%s\n", req.PreviousPlan)
	}
	if req.Feedback != "" {
		fmt.Fprintf(&b, "\nRetour de l'utilisateur sur ce plan (à prendre en compte) :\n%s\n", req.Feedback)
	}
	return b.String()
}

// Analyze mène l'analyse : le modèle appelle des outils jusqu'à proposer
// un plan, dans la limite de MaxSteps. onStep (nil accepté) reçoit chaque
// action, pour le fil du ticket.
func (a *Analyzer) Analyze(ctx context.Context, req tickets.AnalysisRequest, onStep func(tickets.AgentStep)) (string, error) {
	return runLoop(ctx, loopConfig{
		model:           a.Model,
		specs:           ReadOnlySpecs(),
		exec:            func(ctx context.Context, c ToolCall) string { return a.Tools.Execute(c) },
		terminal:        "propose_plan",
		terminalArg:     "plan",
		what:            "l'analyse",
		maxSteps:        orDefault(a.MaxSteps, 16),
		contextChars:    orDefault(a.ContextChars, 18000),
		toolOutputChars: orDefault(a.ToolOutputChars, 3000),
		onStep:          onStep,
	}, a.systemPrompt(), userPrompt(req))
}

// loopConfig paramètre la boucle d'agent commune à l'analyse et au
// développement.
type loopConfig struct {
	model Model
	specs []ToolSpec
	exec  func(ctx context.Context, c ToolCall) string
	// terminal est l'outil qui termine la boucle ; terminalArg l'argument
	// qui porte le résultat (plan, résumé).
	terminal, terminalArg string
	what                  string // "l'analyse", "le développement" (messages d'erreur)
	maxSteps              int
	contextChars          int
	toolOutputChars       int
	onStep                func(tickets.AgentStep)
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// runLoop : le modèle appelle des outils jusqu'à appeler cfg.terminal (ou
// répondre par un texte substantiel), dans la limite de cfg.maxSteps,
// avec compaction du contexte et refus des appels répétés.
func runLoop(ctx context.Context, cfg loopConfig, system, user string) (string, error) {
	msgs := []Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	seen := map[string]bool{} // appels déjà faits (outil + arguments)
	truncations := 0
	specs := cfg.specs
	sterile := 0 // appels stériles d'affilée (aucun résultat, erreur, répétition)

	for step := 0; step < cfg.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("agent: %s interrompu(e) : %w", cfg.what, err)
		}
		reply, err := cfg.model.Chat(ctx, compact(msgs, cfg.contextChars), specs)
		if err != nil {
			// Vu en réel : une réponse trop longue, coupée par le contexte,
			// laisse un appel d'outil au JSON invalide que llama.cpp refuse
			// (erreur 500). Le modèle est prévenu et réessaie, quelques fois.
			if isTruncatedCall(err) && truncations < maxTruncations {
				truncations++
				// À température 0, la même consigne reproduit la même réponse
				// coupée (vu en réel) : on retire l'outil des longues sorties.
				specs = withoutTool(specs, "write_file")
				msgs = append(msgs, Message{Role: "user", Content: "Ta dernière réponse a été coupée (trop longue) : son appel d'outil est illisible. write_file n'est plus disponible : modifie les fichiers avec edit_file, un petit changement à la fois."})
				if cfg.onStep != nil {
					cfg.onStep(tickets.AgentStep{Summary: "Réponse coupée (trop longue) — le modèle est relancé", Detail: err.Error()})
				}
				continue
			}
			return "", fmt.Errorf("agent: modèle : %w", err)
		}
		if result, ok := resultFrom(reply, cfg.terminal, cfg.terminalArg); ok {
			return result, nil
		}
		msgs = append(msgs, reply)
		if len(reply.ToolCalls) == 0 {
			msgs = append(msgs, Message{Role: "user", Content: fmt.Sprintf("Continue avec les outils, ou appelle %s quand tu as terminé.", cfg.terminal)})
			continue
		}
		for _, call := range reply.ToolCalls {
			key := call.Name + " " + call.Arguments
			summary := summarize(call)
			var out string
			if seen[key] && call.Name != "run_tests" && call.Name != "run_checks" {
				// Vu en réel : le même appel en échec répété jusqu'à la
				// limite d'étapes. Le relancer ne changerait rien (sauf
				// tests et vérifications : le code a pu changer entre-temps).
				out = "Appel déjà fait (même outil, mêmes arguments) : son résultat ne changera pas. Change d'approche — list_files pour voir les noms réels, search pour trouver un identifiant — ou termine."
				summary += " (répété, non réexécuté)"
			} else {
				seen[key] = true
				out = truncate(cfg.exec(ctx, call), cfg.toolOutputChars)
			}
			msgs = append(msgs, Message{Role: "tool", ToolCallID: call.ID, Content: out})
			if cfg.onStep != nil {
				cfg.onStep(tickets.AgentStep{Summary: summary, Detail: out})
			}
			if isSterile(out) {
				sterile++
			} else {
				sterile = 0
			}
		}
		// Vu en réel : des dizaines de recherches d'identifiants inventés,
		// jusqu'à la limite d'étapes. Recadrage, puis arrêt anticipé.
		switch {
		case sterile >= stagnationStop:
			return "", fmt.Errorf("agent: %s tourne en rond (%d appels d'affilée sans résultat) — le ticket est peut-être trop gros ou le plan à préciser", cfg.what, sterile)
		case sterile == stagnationSteer:
			msgs = append(msgs, Message{Role: "user", Content: "Tes dernières recherches n'ont rien donné : les noms que tu cherches n'existent probablement pas. Arrête d'inventer des identifiants : liste les dossiers (list_files) et lis les fichiers qui te semblent concernés (read_file), puis agis."})
		}
	}

	// Limite atteinte : une dernière chance, seul l'outil de fin est offert.
	var terminalSpec []ToolSpec
	for _, s := range cfg.specs {
		if s.Name == cfg.terminal {
			terminalSpec = append(terminalSpec, s)
		}
	}
	msgs = append(msgs, Message{Role: "user", Content: fmt.Sprintf("Tu as atteint la limite de %d étapes. Appelle maintenant %s, avec ce que tu as.", cfg.maxSteps, cfg.terminal)})
	reply, err := cfg.model.Chat(ctx, compact(msgs, cfg.contextChars), terminalSpec)
	if err != nil {
		return "", fmt.Errorf("agent: modèle : %w", err)
	}
	if result, ok := resultFrom(reply, cfg.terminal, cfg.terminalArg); ok {
		return result, nil
	}
	return "", fmt.Errorf("agent: %s n'a pas abouti en %d étapes (aucun %s)", cfg.what, cfg.maxSteps, cfg.terminal)
}

func withoutTool(specs []ToolSpec, name string) []ToolSpec {
	out := make([]ToolSpec, 0, len(specs))
	for _, s := range specs {
		if s.Name != name {
			out = append(out, s)
		}
	}
	return out
}

// stagnationSteer / stagnationStop : appels stériles d'affilée avant de
// recadrer le modèle, puis d'arrêter la boucle.
const (
	stagnationSteer = 6
	stagnationStop  = 12
)

// isSterile : un appel qui n'a rien apporté.
func isSterile(out string) bool {
	return out == "(aucun résultat)" || strings.HasPrefix(out, "ERREUR") || strings.HasPrefix(out, "Appel déjà fait")
}

// maxTruncations : réponses coupées tolérées avant d'abandonner.
const maxTruncations = 3

// isTruncatedCall reconnaît le refus par llama.cpp d'un appel d'outil au
// JSON invalide (réponse coupée).
func isTruncatedCall(err error) bool {
	return strings.Contains(err.Error(), "Failed to parse tool call arguments")
}

// resultFrom extrait le résultat d'une réponse : appel à l'outil de fin,
// ou texte libre substantiel sans appel d'outil.
func resultFrom(reply Message, terminal, arg string) (string, bool) {
	for _, call := range reply.ToolCalls {
		if call.Name != terminal {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Arguments), &args); err == nil {
			if v, _ := args[arg].(string); strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), true
			}
		}
	}
	if len(reply.ToolCalls) == 0 && len(strings.TrimSpace(reply.Content)) >= minPlanChars {
		return strings.TrimSpace(reply.Content), true
	}
	return "", false
}

// compact retourne une copie de msgs sous le budget (en caractères) : les
// sorties d'outils les plus anciennes sont remplacées par une note, la
// plus récente reste intacte.
func compact(msgs []Message, budget int) []Message {
	out := append([]Message(nil), msgs...)
	lastTool := -1
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == "tool" {
			lastTool = i
			break
		}
	}
	for i := range out {
		if size(out) <= budget {
			break
		}
		if out[i].Role == "tool" && i != lastTool && out[i].Content != compactedNote {
			out[i].Content = compactedNote
		}
	}
	return out
}

func size(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, c := range m.ToolCalls {
			n += len(c.Name) + len(c.Arguments)
		}
	}
	return n
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // ne pas couper un caractère UTF-8
		cut--
	}
	return s[:cut] + "\n... (sortie tronquée)"
}

// summarize : libellé lisible d'un appel d'outil, pour le fil du ticket.
func summarize(call ToolCall) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &args)
	switch call.Name {
	case "read_file":
		s := fmt.Sprintf("Lit %v", args["path"])
		if args["start_line"] != nil || args["end_line"] != nil {
			s += fmt.Sprintf(" (lignes %v-%v)", orDash(args["start_line"]), orDash(args["end_line"]))
		}
		return s
	case "search":
		s := fmt.Sprintf("Cherche « %v »", args["pattern"])
		if d, _ := args["dir"].(string); d != "" {
			s += " dans " + d
		}
		return s
	case "list_files":
		d, _ := args["dir"].(string)
		if d == "" {
			d = "la racine"
		}
		return "Liste " + d
	case "write_file":
		return "Écrit " + pathArg(args)
	case "edit_file":
		return "Modifie " + pathArg(args)
	case "run_checks":
		return "Lance les vérifications (templ, gofmt, vet, build)"
	case "run_tests":
		p, _ := args["package"].(string)
		if p == "" {
			p = "./..."
		}
		return "Lance les tests " + p
	default:
		return fmt.Sprintf("%s %s", call.Name, call.Arguments)
	}
}

// pathArg : le chemin d'un appel, ou une mention explicite s'il manque
// (vu en réel : "Écrit <nil>").
func pathArg(args map[string]any) string {
	if p, _ := args["path"].(string); p != "" {
		return p
	}
	return "(chemin manquant)"
}

func orDash(v any) any {
	if v == nil {
		return "…"
	}
	return v
}
