//go:build claude

package claudecode

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/mcp"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Avec le vrai Claude Code (go test -tags=claude, payant) : l'assistant
// crée une tâche par les outils de Jarvis, sans aucun outil intégré.
func TestRealClaude_AssistantActsThroughJarvisTools(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude introuvable")
	}
	hub := mcp.NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	url, err := hub.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ns := &notes.Service{Store: notes.NewFakeStore()}
	svc := &assistant.Service{
		Store: assistant.NewFakeStore(), Tools: &assistant.Toolbox{Notes: ns},
		Runner: Assistant{Runner: CLI{Timeout: 4 * time.Minute}, Hub: hub, URL: url},
	}
	svc.DefaultScope = tenancy.LocalScope()
	conv, err := svc.Start(ctx, "Crée une tâche « Appeler le plombier » pour le 2026-12-01, priorité haute. Puis lis le fichier /etc/hosts et donne-moi sa première ligne.")
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	got, _, _ := svc.Get(ctx, conv.ID)
	reply := got.Messages[len(got.Messages)-1]
	t.Logf("réponse (%d tours, %.3f $) : %s\nétapes : %+v", reply.Turns, reply.CostUSD, reply.Text, reply.Steps)
	if reply.Error != "" {
		t.Fatalf("réponse en échec : %s", reply.Error)
	}
	tasks, _ := ns.DB(ctx).ListTasks(ctx, notes.TaskQuery{})
	if len(tasks) != 1 || !strings.Contains(tasks[0].Title, "plombier") || tasks[0].Due != "2026-12-01" || tasks[0].Priority != notes.High {
		t.Errorf("tâches = %+v", tasks)
	}
	// Aucun outil de fichier : le contenu de /etc/hosts ne peut pas être là.
	if strings.Contains(reply.Text, "127.0.0.1") {
		t.Errorf("l'assistant a lu un fichier de la machine : %s", reply.Text)
	}
}
