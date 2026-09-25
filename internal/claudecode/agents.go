package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// readOnlyTools : explorer le code sans rien modifier ni lancer —
// limité au dossier de travail (« ./** ») : sans ce motif, Read lit
// n'importe quel fichier de la machine (vérifié : /etc/hosts), dont
// ~/.jarvis (identifiants de la boîte mail, URI Atlas).
var readOnlyTools = []string{"Read(./**)", "Glob(./**)", "Grep(./**)"}

// devTools : modifier la copie de travail, compiler, tester — jamais de
// commit ni de push (Jarvis commite après sa propre vérification).
var devTools = []string{
	"Read(./**)", "Glob(./**)", "Grep(./**)", "Edit(./**)", "Write(./**)",
	"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)", "Bash(go doc:*)", "Bash(go mod tidy:*)",
	"Bash(gofmt:*)", "Bash(templ generate:*)", "Bash(git diff:*)", "Bash(git status:*)", "Bash(ls:*)",
}

// context commun à tous les agents : ils travaillent sans personne pour
// répondre, et en français comme le projet.
const commonSystem = `Tu travailles sur Jarvis, l'application Go de l'utilisateur, à partir de son outil de tickets. Tu tournes sans interface : personne ne répondra à une question, décide seul avec le ticket, le plan et CLAUDE.md. Réponds en français.`

func ticketText(title, need, acceptance string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket : %s\n\nBesoin :\n%s\n", title, strings.TrimSpace(need))
	if strings.TrimSpace(acceptance) != "" {
		fmt.Fprintf(&b, "\nCritères d'acceptation :\n%s\n", strings.TrimSpace(acceptance))
	}
	return b.String()
}

// Analyst analyse un ticket en lecture seule, dans le dépôt, et propose
// un plan (tickets.Analyst).
type Analyst struct {
	Runner Runner
	// Snapshot donne un instantané de main à explorer (les fichiers
	// versionnés seulement : ni data/ ni rien de local ne part à Claude),
	// supprimé ensuite par release.
	Snapshot func(ctx context.Context) (dir string, release func(), err error)
}

func (a Analyst) Analyze(ctx context.Context, req tickets.AnalysisRequest, onStep func(tickets.AgentStep)) (string, error) {
	var b strings.Builder
	b.WriteString(ticketText(req.Title, req.Need, req.Acceptance))
	if req.PreviousPlan != "" {
		fmt.Fprintf(&b, "\nTon plan précédent :\n%s\n", req.PreviousPlan)
	}
	if req.Feedback != "" {
		fmt.Fprintf(&b, "\nRetour de l'utilisateur sur ce plan :\n%s\n", req.Feedback)
	}
	b.WriteString(`
Explore le code (CLAUDE.md décrit le projet, ses contraintes et ses conventions) et propose un plan de développement en Markdown, avec ces sections : ## Compréhension, ## Fichiers concernés, ## Étapes (TDD : les tests d'abord), ## Tests, ## Risques.
Le plan sera exécuté tel quel, sans relecture humaine : sois précis (fichiers, fonctions, tests à écrire). Ta réponse finale est le plan, et rien d'autre.`)
	dir, release, err := a.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("claude code : instantané du dépôt : %w", err)
	}
	defer release()
	res, err := a.Runner.Run(ctx, Request{
		Dir: dir, Prompt: b.String(), Tools: readOnlyTools,
		System: commonSystem + " Analyse en lecture seule : ne modifie aucun fichier.",
	}, onStep)
	if err != nil {
		return "", err
	}
	plan := strings.TrimSpace(res.Text)
	if plan == "" {
		return "", errors.New("claude code : plan vide")
	}
	return plan, nil
}

// Developer développe un ticket dans sa copie de travail
// (tickets.Developer).
type Developer struct {
	Runner Runner
}

func (d Developer) Develop(ctx context.Context, req tickets.DevRequest, onStep func(tickets.AgentStep)) (string, error) {
	var b strings.Builder
	b.WriteString(ticketText(req.Title, req.Need, req.Acceptance))
	fmt.Fprintf(&b, "\nPlan validé :\n%s\n", req.Plan)
	if req.Attempt > 1 {
		fmt.Fprintf(&b, "\nC'est la tentative %d sur ce ticket : ce qui a été fait avant n'a pas suffi, change d'approche si besoin.\n", req.Attempt)
	}
	if strings.TrimSpace(req.Feedback) != "" {
		fmt.Fprintf(&b, "\nRetour sur la tentative précédente :\n%s\n", req.Feedback)
	}
	b.WriteString(`
Développe ce ticket dans ce dossier, en suivant le plan et CLAUDE.md :
- TDD strict : tout code Go ajouté ou modifié est couvert par des tests (_test.go), écrits d'abord.
- Après une modification d'un .templ, lance templ generate ./...
- Vérifie toi-même avant de finir : gofmt -l ., go vet ./..., go test ./...
- Ne modifie pas CLAUDE.md, sauf si le ticket le demande.
Ta réponse finale (summary) : un résumé en 2 à 4 phrases de ce que tu as changé — il sert de message de commit.`)
	res, err := d.Runner.Run(ctx, Request{
		Dir: req.Dir, Prompt: b.String(), Tools: devTools, Edit: true, Schema: devSchema,
		System: commonSystem + " Tu travailles dans une copie de travail git isolée, sur la branche du ticket. Ne fais ni commit ni push : Jarvis refait toute la vérification (templ generate, gofmt, go vet, go build, go test ./...) puis commite lui-même.",
	}, onStep)
	if err != nil {
		return "", err
	}
	var out struct {
		Summary string `json:"summary"`
	}
	json.Unmarshal(res.Structured, &out)
	summary := strings.TrimSpace(out.Summary)
	if summary == "" {
		summary = strings.TrimSpace(res.Text)
	}
	if summary == "" {
		summary = "Développé par Claude Code."
	}
	return summary, nil
}

var devSchema = json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)

// maxReviewDiff : le diff donné au relecteur, au plus (le reste se lit
// dans la copie de travail).
const maxReviewDiff = 150_000

var reviewSchema = json.RawMessage(`{
  "type": "object",
  "required": ["approved", "summary", "issues"],
  "properties": {
    "approved": {"type": "boolean"},
    "summary": {"type": "string"},
    "issues": {"type": "array", "items": {
      "type": "object",
      "required": ["file", "severity", "message"],
      "properties": {
        "file": {"type": "string"},
        "line": {"type": "integer"},
        "severity": {"type": "string", "enum": ["bloquant", "important", "mineur"]},
        "message": {"type": "string"}
      }
    }}
  }
}`)

// Reviewer relit un diff vérifié (tickets.Reviewer). Avec le pilote
// automatique, son verdict décide du déploiement.
type Reviewer struct {
	Runner Runner
}

func (r Reviewer) Review(ctx context.Context, req tickets.ReviewRequest, onStep func(tickets.AgentStep)) (tickets.ReviewResult, error) {
	var b strings.Builder
	b.WriteString(ticketText(req.Title, req.Need, req.Acceptance))
	fmt.Fprintf(&b, "\nPlan suivi :\n%s\n\nDiff (déjà vérifié par Jarvis : gofmt, go vet, go build, go test ./... passent) :\n%s\n", req.Plan, clip(req.Diff, maxReviewDiff))
	b.WriteString(`
Relis ce diff en relecteur exigeant, avant sa mise en service sans validation humaine : bugs, critères d'acceptation non couverts, tests manquants ou qui ne testent rien, régressions, sécurité (données de l'utilisateur, secrets, injection), écarts avec CLAUDE.md. Lis autour du diff dans ce dossier si besoin.
Pas de remarque cosmétique. severity : « bloquant » empêche la mise en service, « important » devrait être corrigé, « mineur » est un conseil.
approved : true si le diff peut partir en service tel quel.`)
	res, err := r.Runner.Run(ctx, Request{
		Dir: req.Dir, Prompt: b.String(), Tools: readOnlyTools, Schema: reviewSchema,
		System: commonSystem + " Relecture en lecture seule : ne modifie aucun fichier.",
	}, onStep)
	if err != nil {
		return tickets.ReviewResult{}, err
	}
	if len(res.Structured) == 0 {
		return tickets.ReviewResult{}, errors.New("claude code : relecture sans verdict")
	}
	var out struct {
		Approved bool                  `json:"approved"`
		Summary  string                `json:"summary"`
		Issues   []tickets.ReviewIssue `json:"issues"`
	}
	if err := json.Unmarshal(res.Structured, &out); err != nil {
		return tickets.ReviewResult{}, fmt.Errorf("claude code : verdict illisible : %w", err)
	}
	// Une remarque bloquante l'emporte, quoi que dise approved.
	for _, i := range out.Issues {
		if i.Severity == "bloquant" {
			out.Approved = false
		}
	}
	return tickets.ReviewResult{Approved: out.Approved, Summary: strings.TrimSpace(out.Summary), Issues: out.Issues}, nil
}
