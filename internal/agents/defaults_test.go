package agents

import (
	"testing"
)

// Les quatre agents du système, chacun avec un prompt par défaut valide
// (sinon il serait refusé à la première modification) et ses outils.
func TestDefaults_FourValidAgents(t *testing.T) {
	defs := Defaults(Models{Documents: "qwen3-8b Q5_K_M", Tickets: "qwen3-8b"})
	ids := []string{Classification, Extraction, Analysis, Development}
	if len(defs) != len(ids) {
		t.Fatalf("defs = %d, want %d", len(defs), len(ids))
	}
	for i, d := range defs {
		if d.ID != ids[i] || d.Name == "" || d.Role == "" || d.Group == "" || d.Model == "" {
			t.Errorf("def %d = %+v", i, d)
		}
		if err := Validate(d, d.DefaultPrompt); err != nil {
			t.Errorf("%s: default prompt invalid: %v", d.ID, err)
		}
	}
	if len(defs[2].Tools) == 0 || len(defs[3].Tools) <= len(defs[2].Tools) {
		t.Errorf("tools: analysis %v, development %v (development adds write tools)", defs[2].Tools, defs[3].Tools)
	}
	if len(defs[0].Placeholders) != 1 || len(defs[1].Placeholders) != 1 {
		t.Error("classification and extraction each need their placeholder")
	}
}
