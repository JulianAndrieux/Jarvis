package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Reviewer relit le diff vérifié d'un ticket selon les standards du
// projet (jalon 36) : tickets.Reviewer. Lecture seule, terminée par
// submit_review. Vu en réel : deux diffs qui compilaient et passaient les
// tests, mais faux sur le fond (champs sortis du formulaire, mise en page
// demandée non obtenue) — ce que les tests ne voient pas.
type Reviewer struct {
	Model        Model
	ProjectBrief string
	CodeMap      string
	// Instructions : les standards en vigueur (agents.Registry, modifiables
	// dans l'onglet Agents) ; vide : DefaultReviewPrompt.
	Instructions func() string
	// MaxSteps (0 : 12), ContextChars (0 : 18000), ToolOutputChars (0 :
	// 3000) : voir Analyzer. DiffChars borne le diff montré (0 : 12000).
	MaxSteps        int
	ContextChars    int
	ToolOutputChars int
	DiffChars       int
	DisableThinking bool
}

// DefaultReviewPrompt : les standards de relecture par défaut.
// Modifiables depuis l'interface (onglet Agents).
const DefaultReviewPrompt = `Tu es le relecteur de code de l'application Jarvis. Le diff d'un ticket a déjà été compilé et testé automatiquement : ton rôle est ce que les tests ne voient pas. Tu ne modifies rien.

Vérifie, dans cet ordre :
1. Le diff répond-il ENTIÈREMENT au besoin du ticket et à ses critères d'acceptation ? Un besoin traité à moitié est à reprendre.
2. Le comportement existant est-il préservé ? Un champ de saisie doit rester dans le <form> qui l'envoie ; liens, routes et données ne doivent pas casser.
3. Interface : la mise en page demandée est-elle réellement obtenue ? Regarde la structure HTML et les classes CSS (styles dans layout.templ) : une classe sans style ne change rien à l'affichage.
4. Standards du projet : TDD strict (du code Go modifié s'accompagne d'un test), commentaires en français qui expliquent le pourquoi, identifiants en anglais, aucune nouvelle dépendance, jamais de modification des fichiers _templ.go (générés), contenu fourni par l'utilisateur toujours échappé, erreurs Go explicites sans panic.
5. Périmètre : rien d'inutile ni de hors sujet.

Utilise read_file et search pour vérifier le contexte autour du diff (où se ferme un <form>, si une classe CSS existe, ce que fait une fonction appelée).
Termine par submit_review : verdict « acceptable » ou « a_reprendre », un résumé en une ou deux phrases, et chaque problème avec son fichier, sa ligne, sa gravité (bloquant, important, mineur) et une consigne de correction précise. Ne signale jamais un problème que tu n'as pas vérifié : si le diff est bon, dis-le.
`

func (r *Reviewer) instructions() string {
	if r.Instructions != nil {
		if s := strings.TrimSpace(r.Instructions()); s != "" {
			return s + "\n"
		}
	}
	return DefaultReviewPrompt
}

func (r *Reviewer) systemPrompt() string {
	var b strings.Builder
	b.WriteString(r.instructions())
	if r.ProjectBrief != "" {
		b.WriteString("\nContexte du projet :\n" + r.ProjectBrief + "\n")
	}
	writeCodeMap(&b, r.CodeMap)
	if r.DisableThinking {
		b.WriteString("\n/no_think\n")
	}
	return b.String()
}

func (r *Reviewer) userPrompt(req tickets.ReviewRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket : %s\n\nBesoin :\n%s\n", req.Title, req.Need)
	if strings.TrimSpace(req.Acceptance) != "" {
		fmt.Fprintf(&b, "\nCritères d'acceptation :\n%s\n", req.Acceptance)
	}
	fmt.Fprintf(&b, "\nPlan validé :\n%s\n", req.Plan)
	fmt.Fprintf(&b, "\nDiff à relire :\n%s\n", truncate(req.Diff, orDefault(r.DiffChars, 12000)))
	return b.String()
}

// ReviewSpecs : lecture seule, et submit_review pour finir.
func ReviewSpecs() []ToolSpec {
	var specs []ToolSpec
	for _, s := range ReadOnlySpecs() {
		if s.Name != "propose_plan" {
			specs = append(specs, s)
		}
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return append(specs, ToolSpec{
		Name:        "submit_review",
		Description: "Termine la relecture : verdict, résumé et problèmes trouvés.",
		Parameters: map[string]any{"type": "object", "required": []string{"verdict", "summary"}, "properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"acceptable", "a_reprendre"}},
			"summary": str("une ou deux phrases"),
			"issues": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"file":     str("chemin du fichier"),
				"line":     map[string]any{"type": "integer", "description": "ligne concernée (0 : tout le fichier)"},
				"severity": map[string]any{"type": "string", "enum": []string{"bloquant", "important", "mineur"}},
				"message":  str("le problème et la correction attendue"),
			}}},
		}},
	})
}

type verdictArgs struct {
	Verdict string                `json:"verdict"`
	Summary string                `json:"summary"`
	Issues  []tickets.ReviewIssue `json:"issues"`
}

// parseVerdict lit les arguments de submit_review.
func parseVerdict(raw string) (verdictArgs, error) {
	var v verdictArgs
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, fmt.Errorf("arguments illisibles : %v", err)
	}
	if v.Verdict != "acceptable" && v.Verdict != "a_reprendre" {
		return v, fmt.Errorf("verdict manquant ou invalide (%q) : « acceptable » ou « a_reprendre »", v.Verdict)
	}
	return v, nil
}

// Review mène la relecture dans la copie de travail du ticket.
func (r *Reviewer) Review(ctx context.Context, req tickets.ReviewRequest, onStep func(tickets.AgentStep)) (tickets.ReviewResult, error) {
	tools := Tools{Root: req.Dir, MaxOutputChars: orDefault(r.ToolOutputChars, 3000)}
	if err := tools.Accessible(); err != nil {
		return tickets.ReviewResult{}, accessError(err.Error())
	}
	raw, err := runLoop(ctx, loopConfig{
		model:           r.Model,
		specs:           ReviewSpecs(),
		exec:            func(ctx context.Context, c ToolCall) string { return tools.Execute(c) },
		terminal:        "submit_review",
		what:            "la relecture",
		maxSteps:        orDefault(r.MaxSteps, 12),
		contextChars:    orDefault(r.ContextChars, 18000),
		toolOutputChars: orDefault(r.ToolOutputChars, 3000),
		onStep:          onStep,
		check: func(result string) string {
			if _, err := parseVerdict(result); err != nil {
				return "Verdict refusé : " + err.Error() + ". Rappelle submit_review avec verdict, summary et issues."
			}
			return ""
		},
	}, r.systemPrompt(), r.userPrompt(req))
	if err != nil {
		return tickets.ReviewResult{}, err
	}
	v, err := parseVerdict(raw)
	if err != nil {
		return tickets.ReviewResult{}, fmt.Errorf("agent: verdict de relecture illisible : %w", err)
	}
	res := tickets.ReviewResult{Approved: v.Verdict == "acceptable", Summary: strings.TrimSpace(v.Summary), Issues: v.Issues}
	// Une remarque bloquante contredit un « acceptable » : jamais accepté.
	for _, i := range v.Issues {
		if i.Severity == "bloquant" {
			res.Approved = false
		}
	}
	return res, nil
}
