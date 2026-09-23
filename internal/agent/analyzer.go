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
}

// minPlanChars : en dessous, une réponse en texte libre n'est pas un plan.
const minPlanChars = 40

const compactedNote = "[sortie déjà lue, retirée pour tenir dans le contexte — relis le fichier si besoin]"

func (a *Analyzer) systemPrompt() string {
	var b strings.Builder
	b.WriteString(`Tu es l'agent de développement de l'application Jarvis. Ta mission maintenant : ANALYSER un ticket, en lecture seule — tu ne modifies rien.

Explore le code avec les outils list_files, search et read_file. Lis ce qui est nécessaire, pas tout : commence par chercher les noms utiles, puis lis les passages concernés.
Quand tu as compris, appelle propose_plan avec un plan en Markdown, en français, avec exactement ces sections :
## Compréhension
## Fichiers concernés (uniquement des chemins que tu as vus)
## Tests à écrire d'abord (le projet est en TDD strict)
## Étapes
## Risques et questions
N'invente jamais un fichier, un type ou une fonction que tu n'as pas lu. Si le ticket est trop gros ou ambigu, dis-le dans « Risques et questions » plutôt que de deviner.

Conventions du code : les identifiants (types, fonctions, noms de fichiers) sont en anglais, les commentaires et les textes affichés en français. Cherche donc des identifiants anglais (Document, List, Row, Page...) plutôt que des mots français. Les pages web sont des gabarits templ (fichiers .templ ; les fichiers _templ.go sont générés, ne les lis pas). Si un outil répond ERREUR, lis le message : il propose souvent le bon chemin.
`)
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
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 16
	}
	msgs := []Message{
		{Role: "system", Content: a.systemPrompt()},
		{Role: "user", Content: userPrompt(req)},
	}
	specs := ReadOnlySpecs()
	seen := map[string]bool{} // appels déjà faits (outil + arguments)

	for step := 0; step < maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("agent: analyse interrompue : %w", err)
		}
		reply, err := a.Model.Chat(ctx, a.compact(msgs), specs)
		if err != nil {
			return "", fmt.Errorf("agent: modèle : %w", err)
		}
		if plan, ok := planFrom(reply); ok {
			return plan, nil
		}
		msgs = append(msgs, reply)
		if len(reply.ToolCalls) == 0 {
			msgs = append(msgs, Message{Role: "user", Content: "Continue avec les outils, ou appelle propose_plan si tu as assez d'éléments."})
			continue
		}
		for _, call := range reply.ToolCalls {
			key := call.Name + " " + call.Arguments
			summary := summarize(call)
			var out string
			if seen[key] {
				// Vu en réel : le même appel en échec répété jusqu'à la
				// limite d'étapes. Le relancer ne changerait rien.
				out = "Appel déjà fait (même outil, mêmes arguments) : son résultat ne changera pas. Change d'approche — list_files pour voir les noms réels, search pour trouver un identifiant — ou propose ton plan."
				summary += " (répété, non réexécuté)"
			} else {
				seen[key] = true
				out = truncate(a.Tools.Execute(call), a.toolOutputChars())
			}
			msgs = append(msgs, Message{Role: "tool", ToolCallID: call.ID, Content: out})
			if onStep != nil {
				onStep(tickets.AgentStep{Summary: summary, Detail: out})
			}
		}
	}

	// Limite atteinte : une dernière chance, seul propose_plan est offert.
	msgs = append(msgs, Message{Role: "user", Content: fmt.Sprintf("Tu as atteint la limite de %d étapes. Propose maintenant ton plan avec propose_plan, avec ce que tu sais.", maxSteps)})
	reply, err := a.Model.Chat(ctx, a.compact(msgs), specs[len(specs)-1:])
	if err != nil {
		return "", fmt.Errorf("agent: modèle : %w", err)
	}
	if plan, ok := planFrom(reply); ok {
		return plan, nil
	}
	return "", fmt.Errorf("agent: l'analyse n'a pas abouti en %d étapes (aucun plan proposé)", maxSteps)
}

// planFrom extrait un plan d'une réponse : appel à propose_plan, ou texte
// libre substantiel sans appel d'outil.
func planFrom(reply Message) (string, bool) {
	for _, call := range reply.ToolCalls {
		if call.Name != "propose_plan" {
			continue
		}
		var args struct {
			Plan string `json:"plan"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err == nil && strings.TrimSpace(args.Plan) != "" {
			return strings.TrimSpace(args.Plan), true
		}
	}
	if len(reply.ToolCalls) == 0 && len(strings.TrimSpace(reply.Content)) >= minPlanChars {
		return strings.TrimSpace(reply.Content), true
	}
	return "", false
}

func (a *Analyzer) toolOutputChars() int {
	if a.ToolOutputChars > 0 {
		return a.ToolOutputChars
	}
	return 3000
}

// compact retourne une copie de msgs sous le budget ContextChars : les
// sorties d'outils les plus anciennes sont remplacées par une note, la
// plus récente reste intacte.
func (a *Analyzer) compact(msgs []Message) []Message {
	budget := a.ContextChars
	if budget <= 0 {
		budget = 18000
	}
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
	default:
		return fmt.Sprintf("%s %s", call.Name, call.Arguments)
	}
}

func orDash(v any) any {
	if v == nil {
		return "…"
	}
	return v
}
