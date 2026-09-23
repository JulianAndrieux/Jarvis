package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Developer développe un ticket dans sa copie de travail (jalon 28) :
// tickets.Developer. Même boucle que l'analyse, outils d'écriture en plus
// (DevTools), terminée par finish. La vérification finale (gofmt, vet,
// toute la suite de tests) n'est pas confiée au modèle : le gestionnaire
// de tickets la refait lui-même.
type Developer struct {
	Model        Model
	Checker      Checker
	ProjectBrief string
	// MaxSteps (0 : 40), ContextChars (0 : 18000), ToolOutputChars (0 :
	// 3000) : voir Analyzer.
	MaxSteps        int
	ContextChars    int
	ToolOutputChars int
	DisableThinking bool
}

func (d *Developer) systemPrompt() string {
	var b strings.Builder
	b.WriteString(`Tu es l'agent de développement de l'application Jarvis. Ta mission maintenant : DÉVELOPPER un ticket dont le plan a été validé, dans une copie de travail isolée (une branche git dédiée ; le code de l'application en service n'est pas touché).

Méthode imposée (le projet est en TDD strict) :
1. Lis les fichiers concernés avant de les modifier (read_file ; search pour trouver un identifiant).
2. Écris d'abord le test (edit_file ou write_file dans un fichier _test.go).
3. Lance run_tests sur le paquet concerné (ex. ./internal/webapp/) et vérifie qu'il ÉCHOUE.
4. Implémente le minimum pour le faire passer (edit_file de préférence : extrait exact, indentation comprise).
5. Relance run_tests jusqu'au SUCCÈS, puis run_checks (templ, gofmt, go vet, go build) jusqu'au SUCCÈS.
6. Appelle finish avec un résumé en français des changements.

Interdits : go.mod et go.sum (aucune nouvelle dépendance), les fichiers _templ.go (générés : modifie le .templ, run_checks régénère), CLAUDE.md. Reste dans le périmètre du plan : ne réécris pas ce qui n'est pas demandé. Les commentaires du code sont en français et expliquent le pourquoi.

Conventions du code : identifiants en anglais, commentaires et textes affichés en français, pages web en gabarits templ (.templ). Si un outil répond ERREUR, lis le message : il montre souvent le bon chemin ou les lignes réelles.
`)
	if d.ProjectBrief != "" {
		b.WriteString("\nContexte du projet :\n" + d.ProjectBrief + "\n")
	}
	if d.DisableThinking {
		b.WriteString("\n/no_think\n")
	}
	return b.String()
}

// temperatureSetter : un modèle dont on peut régler la température
// (HTTPModel).
type temperatureSetter interface {
	WithTemperature(t float64) Model
}

// retryTemperature : aléa des tentatives après la première.
const retryTemperature = 0.5

func devUserPrompt(req tickets.DevRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket : %s\n\nBesoin :\n%s\n", req.Title, req.Need)
	if strings.TrimSpace(req.Acceptance) != "" {
		fmt.Fprintf(&b, "\nCritères d'acceptation :\n%s\n", req.Acceptance)
	}
	fmt.Fprintf(&b, "\nPlan validé :\n%s\n", req.Plan)
	if req.Feedback != "" {
		fmt.Fprintf(&b, "\nÀ corriger (tentative précédente) :\n%s\n", req.Feedback)
	}
	return b.String()
}

// Develop développe le ticket dans req.Dir et retourne le résumé de
// l'agent.
func (d *Developer) Develop(ctx context.Context, req tickets.DevRequest, onStep func(tickets.AgentStep)) (string, error) {
	tools := DevTools{Tools: Tools{Root: req.Dir}, Checker: d.Checker}
	// Vu en réel : relancer à température 0 rejoue exactement le même
	// déroulé. À partir de la deuxième tentative, un peu d'aléa.
	model := d.Model
	if ts, ok := model.(temperatureSetter); ok {
		temp := 0.0
		if req.Attempt > 1 {
			temp = retryTemperature
		}
		model = ts.WithTemperature(temp)
	}
	return runLoop(ctx, loopConfig{
		model:           model,
		specs:           DevSpecs(),
		exec:            tools.Execute,
		terminal:        "finish",
		terminalArg:     "summary",
		what:            "le développement",
		maxSteps:        orDefault(d.MaxSteps, 40),
		contextChars:    orDefault(d.ContextChars, 18000),
		toolOutputChars: orDefault(d.ToolOutputChars, 3000),
		onStep:          onStep,
	}, d.systemPrompt(), devUserPrompt(req))
}
