package main

import (
	"strings"
	"testing"
)

// Jalon 38 : une version lancée pour une capture est isolée — port libre,
// collections jetables, ni modèles, ni dossier surveillé, ni copie locale,
// ni développement ni déploiement de tickets.
func TestVisualArgs_Isolated(t *testing.T) {
	current := []string{"--addr", "127.0.0.1:8090", "--mongo-collection", "jobs", "--tickets-collection", "tickets",
		"--models-file", "/home/x/.jarvis/models.json", "--watch-dir", "/home/x/Inbox", "--out-dir", "/data",
		"--mail-config", "/home/x/.jarvis/mail.json", "--mail-collection", "emails",
		"--notes-collection", "notes", "--tasks-collection", "tasks"}
	got := strings.Join(visualArgs(current, "127.0.0.1:9999", "jobs", "tickets"), " ")
	for _, want := range []string{"127.0.0.1:9999", "jobs_visualcheck", "tickets_visualcheck", "--models-file=", "--watch-dir=", "--out-dir=", "--agent-dev=false", "--deploy=false", "--mail-config=", "--mail-collection=emails_check", "--ticket-pickup=0", "--notes-collection=notes_check", "--tasks-collection=tasks_check"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"8090", "models.json", "Inbox", "/data", "mail.json", "--mail-collection emails", "--notes-collection notes", "--tasks-collection tasks"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("args keep %q: %s", unwanted, got)
		}
	}
}

// L'essai à blanc d'un déploiement : même isolement (jamais la boîte mail,
// ni les modèles, ni les vraies collections). Le tableau de bord (ticket
// "Revoir ordre des sections") lit désormais notes et tâches : elles sont
// isolées comme le reste.
func TestSmokeArgs_Isolated(t *testing.T) {
	current := []string{"--addr", "127.0.0.1:8090", "--mongo-collection", "jobs", "--tickets-collection", "tickets",
		"--models-file", "/home/x/.jarvis/models.json", "--mail-config", "/home/x/.jarvis/mail.json", "--mail-collection", "emails",
		"--notes-collection", "notes", "--tasks-collection", "tasks"}
	got := strings.Join(smokeArgs(current, "127.0.0.1:9999", "jobs", "tickets"), " ")
	for _, want := range []string{"127.0.0.1:9999", "jobs_deploycheck", "tickets_deploycheck", "--models-file=", "--mail-config=", "--mail-collection=emails_check", "--watch-dir=", "--ticket-pickup=0", "--notes-collection=notes_check", "--tasks-collection=tasks_check"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"8090", "models.json", "mail.json", "--mail-collection emails", "--notes-collection notes", "--tasks-collection tasks"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("args keep %q: %s", unwanted, got)
		}
	}
}

// Une instance de contrôle (essai à blanc du déploiement, capture) ne doit
// toucher à rien de réel : ni la boîte mail, ni le journal, ni les
// changesets, ni les comptes — et surtout, elle ne doit pas exiger de
// connexion, sinon toutes ses pages répondraient une redirection vers
// /login et aucun déploiement ne passerait jamais.
func TestCheckIsolation_NeTouchePasAuReel(t *testing.T) {
	for name, argsOf := range map[string]func([]string, string, string, string) []string{
		"essai à blanc":      smokeArgs,
		"relecture visuelle": visualArgs,
	} {
		t.Run(name, func(t *testing.T) {
			got := argsOf([]string{
				"--mongo-collection", "jobs",
				"--auth-config", "/Users/julian/.jarvis/oauth.json",
				"--changes-collection", "changes",
				"--changeset-collection", "changeset",
				"--accounts-prefix", "accounts_",
				"--mail-config", "/Users/julian/.jarvis/mail.json",
			}, "127.0.0.1:9999", "jobs", "tickets")
			joined := strings.Join(got, " ")
			for _, forbidden := range []string{
				"/Users/julian/.jarvis/oauth.json",
				"/Users/julian/.jarvis/mail.json",
			} {
				if strings.Contains(joined, forbidden) {
					t.Errorf("les options contiennent encore %q :\n%s", forbidden, joined)
				}
			}
			for _, want := range []string{
				"--auth-config=", // vidé : pas de connexion exigée
				"changes_check",
				"changeset_check",
				"accounts_check_",
				"emails_check",
			} {
				if !strings.Contains(joined, want) {
					t.Errorf("les options doivent contenir %q :\n%s", want, joined)
				}
			}
			// Et les collections réelles ne doivent pas être visées.
			if strings.Contains(joined, "--changes-collection=changes ") || strings.HasSuffix(joined, "--changes-collection=changes") {
				t.Errorf("le journal réel est visé :\n%s", joined)
			}
		})
	}
}
