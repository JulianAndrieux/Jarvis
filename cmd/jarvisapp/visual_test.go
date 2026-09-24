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
		"--models-file", "/home/x/.jarvis/models.json", "--watch-dir", "/home/x/Inbox", "--out-dir", "/data"}
	got := strings.Join(visualArgs(current, "127.0.0.1:9999", "jobs", "tickets"), " ")
	for _, want := range []string{"127.0.0.1:9999", "jobs_visualcheck", "tickets_visualcheck", "--models-file=", "--watch-dir=", "--out-dir=", "--agent-dev=false", "--deploy=false"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"8090", "models.json", "Inbox", "/data"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("args keep %q: %s", unwanted, got)
		}
	}
}
