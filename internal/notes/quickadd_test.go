package notes

import (
	"testing"
	"time"
)

// Mercredi 23 septembre 2026.
var wednesday = time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)

// Saisie rapide : les mots-clés en fin de saisie donnent l'échéance et la
// priorité, le reste est le titre.
func TestParseQuickAdd(t *testing.T) {
	cases := []struct {
		in, title, due string
		prio           Priority
	}{
		{"Appeler le notaire demain !", "Appeler le notaire", "2026-09-24", High},
		{"Payer la facture EDF 30/09", "Payer la facture EDF", "2026-09-30", Normal},
		{"Renouveler le passeport 15/03", "Renouveler le passeport", "2027-03-15", Normal}, // date passée : l'an prochain
		{"Envoyer le devis 02/10/2026 !basse", "Envoyer le devis", "2026-10-02", Low},
		{"Réunion équipe lundi", "Réunion équipe", "2026-09-28", Normal},
		{"Point hebdo mercredi", "Point hebdo", "2026-09-30", Normal}, // même jour : la semaine prochaine
		{"Courses aujourd'hui", "Courses", "2026-09-23", Normal},
		{"Relancer après-demain !haute", "Relancer", "2026-09-25", High},
		{"Lire le rapport", "Lire le rapport", "", Normal},
		// Seuls les mots-clés en fin de saisie comptent.
		{"Préparer demain la réunion", "Préparer demain la réunion", "", Normal},
		// Date impossible : laissée dans le titre.
		{"Voir 31/02", "Voir 31/02", "", Normal},
		// Rien qu'un mot-clé : il reste le titre.
		{"demain", "demain", "", Normal},
	}
	for _, c := range cases {
		got := ParseQuickAdd(c.in, wednesday)
		if got.Title != c.title || got.Due != c.due || got.Priority != c.prio {
			t.Errorf("ParseQuickAdd(%q) = {%q %q %q}, want {%q %q %q}", c.in, got.Title, got.Due, got.Priority, c.title, c.due, c.prio)
		}
	}
}
