package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/deploy"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// baseRestorer : ce qu'il faut de git pour défaire un déploiement —
// workspace.Manager en production.
type baseRestorer interface {
	BaseHead(ctx context.Context) (string, error)
	RestoreBase(ctx context.Context, prev, message string) (string, error)
}

// settleDeployment règle, au démarrage, un déploiement laissé par le
// processus précédent (jalon 30) : la nouvelle version le confirme ;
// l'ancienne, rétablie par le lanceur, ramène main à l'état d'avant (si
// personne ne l'a bougé depuis) et renvoie le ticket en revue. Retourne
// un message pour le journal ("" : aucun déploiement en attente).
func settleDeployment(ctx context.Context, markerPath string, tm *tickets.Manager, git baseRestorer) (string, error) {
	m, ok, err := deploy.ReadMarker(markerPath)
	if err != nil || !ok {
		return "", err
	}
	defer deploy.RemoveMarker(markerPath)
	if !m.RolledBack {
		if err := tm.ConfirmDeployment(ctx, m.TicketID, m.Commit); err != nil {
			return "", err
		}
		return fmt.Sprintf("déploiement du ticket %s confirmé (main à %.7s)", m.TicketID, m.Commit), nil
	}

	reason := m.Reason
	switch head, err := git.BaseHead(ctx); {
	case err != nil:
		reason += "\n\nmain non ramené en arrière : " + err.Error()
	case head != m.Commit:
		reason += fmt.Sprintf("\n\nmain a bougé depuis le déploiement (%.7s au lieu de %.7s) : non ramené en arrière, à vérifier à la main.", head, m.Commit)
	default:
		first, _, _ := strings.Cut(m.Reason, "\n")
		c, err := git.RestoreBase(ctx, m.PrevCommit, fmt.Sprintf("Retour arrière du déploiement du ticket %s\n\n%s", m.TicketID, first))
		if err != nil {
			reason += "\n\nmain non ramené en arrière : " + err.Error()
		} else {
			reason += fmt.Sprintf("\n\nmain ramené à l'état d'avant le déploiement (commit %.7s).", c)
		}
	}
	if err := tm.DeploymentRolledBack(ctx, m.TicketID, reason); err != nil {
		return "", err
	}
	return fmt.Sprintf("déploiement du ticket %s annulé, ancienne version en service", m.TicketID), nil
}

// smokeTest : l'essai à blanc d'une nouvelle version — mêmes options que
// le processus courant, mais port libre, collections jetables, ni dossier
// surveillé, ni copie locale, ni marqueur de déploiement, ni gestion des
// modèles.
func smokeTest(jobsCollection, ticketsCollection string) func(ctx context.Context, binary string) (string, error) {
	return func(ctx context.Context, binary string) (string, error) {
		addr, err := deploy.FreeAddr()
		if err != nil {
			return "", err
		}
		args := smokeArgs(os.Args[1:], addr, jobsCollection, ticketsCollection)
		s := deploy.Smoke{Args: args, Env: os.Environ(), Addr: addr, Paths: []string{"/", "/documents", "/tickets", "/admin/architecture"}}
		return s.Run(ctx, binary)
	}
}

// smokeArgs : les options de l'essai à blanc.
func smokeArgs(current []string, addr, jobsCollection, ticketsCollection string) []string {
	return deploy.OverrideFlags(current, withMailIsolation(map[string]string{
		"addr":               addr,
		"mongo-collection":   jobsCollection + "_deploycheck",
		"tickets-collection": ticketsCollection + "_deploycheck",
		"watch-dir":          "",
		"out-dir":            "",
		"deploy-marker":      filepath.Join(os.TempDir(), "jarvis-deploycheck.json"),
		// Jalon 37 : l'essai à blanc ne touche jamais aux modèles (il
		// arrêterait ceux de l'application en service).
		"models-file": "",
		// Jalon 41 : ni relève des tickets, ni Claude Code en essai.
		"ticket-pickup": "0",
	}))
}

// busyReason : pourquoi l'application ne peut pas redémarrer maintenant.
func busyReason(jobs *webapp.JobManager, tm *tickets.Manager) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		var reasons []string
		n := 0
		for _, st := range []webapp.Status{webapp.StatusRunning, webapp.StatusPending} {
			l, err := jobs.List(ctx, webapp.ListQuery{Status: st, SummaryOnly: true})
			if err != nil {
				return "état des documents inconnu : " + err.Error()
			}
			n += len(l)
		}
		if n > 0 {
			reasons = append(reasons, fmt.Sprintf("%d document(s) en traitement", n))
		}
		k := 0
		for _, st := range []tickets.Status{tickets.Analyzing, tickets.Developing} {
			l, err := tm.List(ctx, st)
			if err != nil {
				return "état des tickets inconnu : " + err.Error()
			}
			k += len(l)
		}
		if k > 0 {
			reasons = append(reasons, fmt.Sprintf("%d autre(s) ticket(s) en cours", k))
		}
		return strings.Join(reasons, ", ")
	}
}
