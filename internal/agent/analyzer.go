package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
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
	// CodeMap : les paquets du dépôt et leur rôle (PackageMap).
	CodeMap string
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
	writeCodeMap(&b, a.CodeMap)
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
	if err := a.Tools.Accessible(); err != nil {
		return "", accessError(err.Error())
	}
	tools := a.Tools
	if tools.MaxOutputChars == 0 {
		tools.MaxOutputChars = orDefault(a.ToolOutputChars, 3000)
	}
	return runLoop(ctx, loopConfig{
		model:           a.Model,
		specs:           ReadOnlySpecs(),
		exec:            func(ctx context.Context, c ToolCall) string { return tools.Execute(c) },
		terminal:        "propose_plan",
		terminalArg:     "plan",
		what:            "l'analyse",
		maxSteps:        orDefault(a.MaxSteps, 16),
		contextChars:    orDefault(a.ContextChars, 18000),
		toolOutputChars: orDefault(a.ToolOutputChars, 3000),
		onStep:          onStep,
		textResult:      true,
		check:           a.checkPlan,
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
	// textResult : une réponse en texte (sans outil) vaut résultat — pour
	// l'analyse (le plan peut arriver en texte), jamais pour le
	// développement (vu en réel : tentative close sans modification).
	textResult bool
	// check, s'il est donné, examine le résultat : un message non vide le
	// renvoie au modèle (maxChecks fois au plus), qui doit le corriger.
	check func(result string) string
}

// maxChecks : renvois d'un résultat au modèle avant de l'accepter tel
// quel (la validation humaine reste le dernier filet).
const maxChecks = 2

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
	// sterileSeen : appels déjà faits sans résultat (outil + arguments) —
	// les répéter ne changerait rien. Un appel utile peut être refait (la
	// compaction retire les anciennes sorties en disant « relis si
	// besoin » : vu en réel, la relecture refusée a bloqué le modèle).
	sterileSeen := map[string]bool{}
	checks := 0
	truncated := false // un write_file coupé dans la dernière réponse
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
		if result, ok := resultFrom(reply, cfg.terminal, cfg.terminalArg, cfg.textResult); ok {
			msg := ""
			if cfg.check != nil && checks < maxChecks {
				msg = cfg.check(result)
			}
			if msg == "" {
				return result, nil
			}
			checks++
			msgs = append(msgs, historySafe(reply), feedbackFor(reply, cfg.terminal, msg))
			if cfg.onStep != nil {
				cfg.onStep(tickets.AgentStep{Summary: "Résultat renvoyé au modèle pour correction", Detail: msg})
			}
			continue
		}
		msgs = append(msgs, historySafe(reply))
		if len(reply.ToolCalls) == 0 {
			msgs = append(msgs, Message{Role: "user", Content: fmt.Sprintf("Continue avec les outils, ou appelle %s quand tu as terminé.", cfg.terminal)})
			continue
		}
		for _, call := range reply.ToolCalls {
			key := call.Name + " " + call.Arguments
			summary := summarize(call)
			var out string
			if sterileSeen[key] && call.Name != "run_tests" && call.Name != "run_checks" {
				// Vu en réel : le même appel en échec répété jusqu'à la
				// limite d'étapes. Le relancer ne changerait rien (sauf
				// tests et vérifications : le code a pu changer entre-temps).
				out = "Appel déjà fait (même outil, mêmes arguments) : son résultat ne changera pas. Change d'approche — list_files pour voir les noms réels, search pour trouver un identifiant — ou termine."
				summary += " (répété, non réexécuté)"
			} else {
				out = truncate(cfg.exec(ctx, call), cfg.toolOutputChars)
				switch {
				case isSterile(out):
					sterileSeen[key] = true
				case strings.HasPrefix(out, "Modifié") || strings.HasPrefix(out, "Écrit"):
					// Le code a changé : ce qui ne donnait rien peut aboutir.
					sterileSeen = map[string]bool{}
				}
			}
			msgs = append(msgs, Message{Role: "tool", ToolCallID: call.ID, Content: out})
			if cfg.onStep != nil {
				cfg.onStep(tickets.AgentStep{Summary: summary, Detail: out})
			}
			if detail, ok := strings.CutPrefix(out, accessDeniedPrefix); ok {
				return "", accessError(detail)
			}
			// Vu en réel : une réécriture coupée arrive aussi comme un
			// write_file aux arguments tronqués — même remède qu'une coupure
			// signalée par le serveur.
			if call.Name == "write_file" && !json.Valid([]byte(call.Arguments)) && truncations < maxTruncations {
				truncations++
				specs = withoutTool(specs, "write_file")
				truncated = true
			}
			if isSterile(out) {
				sterile++
			} else {
				sterile = 0
			}
		}
		if truncated {
			truncated = false
			msgs = append(msgs, Message{Role: "user", Content: "Ta réponse a été coupée (trop longue) : write_file n'est plus disponible. Modifie les fichiers avec edit_file, un petit changement à la fois (old : quelques lignes réelles, new : leur nouvelle version)."})
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
	if result, ok := resultFrom(reply, cfg.terminal, cfg.terminalArg, cfg.textResult); ok {
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

// feedbackFor : la réponse à un résultat renvoyé — réponse de l'outil de
// fin s'il a été appelé (l'API attend une réponse à chaque appel),
// message de l'utilisateur sinon.
func feedbackFor(reply Message, terminal, msg string) Message {
	for _, c := range reply.ToolCalls {
		if c.Name == terminal {
			return Message{Role: "tool", ToolCallID: c.ID, Content: msg}
		}
	}
	return Message{Role: "user", Content: msg}
}

// planPathRe : un chemin de fichier cité entre accents graves dans un plan.
var planPathRe = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:go|templ|md|mod|sh|py|json|css|js|html))`")

// checkPlan : vu en réel, un plan citait un fichier inexistant (cherché
// sans succès). Les chemins cités doivent exister, sauf ceux annoncés
// comme nouveaux sur leur ligne.
func (a *Analyzer) checkPlan(plan string) string {
	var missing []string
	for _, line := range strings.Split(plan, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "nouveau") || strings.Contains(lower, "à créer") || strings.Contains(lower, "a creer") {
			continue
		}
		for _, m := range planPathRe.FindAllStringSubmatch(line, -1) {
			abs, err := a.Tools.resolve(m[1])
			if err != nil {
				missing = append(missing, m[1])
				continue
			}
			if _, err := os.Stat(abs); err != nil && !slices.Contains(missing, m[1]) {
				missing = append(missing, m[1])
			}
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "Ces fichiers cités dans le plan n'existent pas : " + strings.Join(missing, ", ") + ". Corrige le plan avec des chemins que tu as vus (list_files, search) — un fichier à créer s'écrit avec « (nouveau) » sur sa ligne — puis rappelle propose_plan."
}

// accessError : le dépôt est illisible pour Jarvis — une panne de
// l'environnement, pas du ticket ni du modèle (vu en réel : macOS avait
// retiré l'accès au dossier Documents, l'échec disait « le ticket est
// peut-être trop gros »).
func accessError(detail string) error {
	return fmt.Errorf("agent: Jarvis n'a pas accès au dépôt (%s). Ce n'est pas le ticket : sur macOS, vérifie que Jarvis peut accéder au dossier du dépôt (Réglages Système › Confidentialité et sécurité › Fichiers et dossiers), puis relance Jarvis et le ticket", detail)
}

// historySafe : la réponse telle qu'elle sera renvoyée au modèle dans
// l'historique, arguments JSON invalides remplacés par {}. Vu en réel :
// des arguments coupés (le modèle bouclait dans un write_file), renvoyés
// tels quels, font répondre 500 à llama.cpp à chaque requête suivante —
// il relit les appels passés. L'outil, lui, reçoit les arguments d'origine
// et répond par une ERREUR que le modèle voit.
func historySafe(reply Message) Message {
	calls := make([]ToolCall, len(reply.ToolCalls))
	for i, c := range reply.ToolCalls {
		if !json.Valid([]byte(c.Arguments)) {
			c.Arguments = "{}"
		}
		calls[i] = c
	}
	reply.ToolCalls = calls
	return reply
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
func resultFrom(reply Message, terminal, arg string, textResult bool) (string, bool) {
	for _, call := range reply.ToolCalls {
		if call.Name != terminal {
			continue
		}
		// arg vide : le résultat est l'ensemble des arguments (JSON), comme
		// pour submit_review.
		if arg == "" {
			if json.Valid([]byte(call.Arguments)) {
				return call.Arguments, true
			}
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Arguments), &args); err == nil {
			if v, _ := args[arg].(string); strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), true
			}
		}
	}
	if textResult && len(reply.ToolCalls) == 0 && len(strings.TrimSpace(reply.Content)) >= minPlanChars {
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
