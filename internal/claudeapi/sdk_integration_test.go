//go:build anthropic

package claudeapi

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Contre la vraie API (payant, quelques centimes) :
//
//	ANTHROPIC_API_KEY=… go test -tags=anthropic ./internal/claudeapi/
//
// Vérifie ce que les faux ne peuvent pas : que la requête est acceptée
// telle que le SDK l'envoie (repli, cache, effort), qu'un appel d'outil
// revient lisible, et que le message du modèle rejoué tel quel — blocs de
// réflexion compris — est accepté au tour suivant.
func TestSDKClient_RealToolRoundTrip(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := NewSDKClient(Config{Model: os.Getenv("JARVIS_ASSISTANT_MODEL"), Effort: "low"})
	req := Request{
		System: "Tu es un assistant de test. Pour connaître une tâche, appelle l'outil list_tasks ; ne réponds jamais de mémoire.",
		Tools: []Tool{{Name: "list_tasks", Description: "Liste les tâches de l'utilisateur.", Schema: map[string]any{
			"type": "object", "properties": map[string]any{"filter": map[string]any{"type": "string", "enum": []string{"ouvertes", "toutes"}}},
			"additionalProperties": false,
		}}},
		Messages: []Message{{User: "Quelle est ma seule tâche ouverte ? Réponds en une phrase."}},
	}
	first, err := c.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stop != StopToolUse || len(first.Calls) != 1 || first.Calls[0].Name != "list_tasks" {
		t.Fatalf("premier tour = %+v", first)
	}
	req.Messages = append(req.Messages, Message{Assistant: first.Raw},
		Message{Results: []Result{{CallID: first.Calls[0].ID, Content: "1 tâche :\n- [ ] Arroser le ficus"}}})
	second, err := c.Complete(ctx, req)
	if err != nil {
		t.Fatalf("second tour (message rejoué) : %v", err)
	}
	if second.Stop != StopEnd || !strings.Contains(strings.ToLower(second.Text), "ficus") {
		t.Errorf("second tour = %+v", second)
	}
	t.Logf("modèle %s, jetons : %+v puis %+v", second.Model, first.Usage, second.Usage)
}
