package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testDefs() []Definition {
	return []Definition{
		{ID: "classification", Name: "Classification", DefaultPrompt: "Types :\n{{types}}\nRéponds.", Placeholders: []Placeholder{{Name: "types", Meaning: "liste des types"}}},
		{ID: "analysis", Name: "Analyse de ticket", DefaultPrompt: "Analyse le ticket."},
	}
}

func newTestRegistry(t *testing.T) (*Registry, *FakeStore) {
	t.Helper()
	store := NewFakeStore()
	r, err := NewRegistry(context.Background(), store, testDefs())
	if err != nil {
		t.Fatal(err)
	}
	return r, store
}

func TestRegistry_DefaultPromptUntilCustomised(t *testing.T) {
	r, _ := newTestRegistry(t)
	if got := r.Prompt("analysis"); got != "Analyse le ticket." {
		t.Errorf("Prompt = %q, want the default", got)
	}
	a, ok := r.Get("analysis")
	if !ok || a.Custom || a.Name != "Analyse de ticket" {
		t.Errorf("Get = %+v, %v", a, ok)
	}
}

// Une modification s'applique au prochain appel, sans redémarrage, et
// survit à un redémarrage (relue depuis le Store).
func TestRegistry_SetPromptAppliesNowAndPersists(t *testing.T) {
	r, store := newTestRegistry(t)
	prompt := r.PromptFunc("analysis")
	if err := r.SetPrompt(context.Background(), "analysis", "  Nouveau prompt.  "); err != nil {
		t.Fatal(err)
	}
	if got := prompt(); got != "Nouveau prompt." {
		t.Errorf("prompt() = %q, want the new prompt (trimmed)", got)
	}
	again, err := NewRegistry(context.Background(), store, testDefs())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := again.Get("analysis")
	if a.Prompt != "Nouveau prompt." || !a.Custom || a.UpdatedAt.IsZero() {
		t.Errorf("after restart = %+v", a)
	}
}

// Chaque modification garde la version précédente, la plus récente
// d'abord, bornée.
func TestRegistry_HistoryKeepsPreviousVersions(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	for i := 1; i <= MaxHistory+3; i++ {
		if err := r.SetPrompt(ctx, "analysis", strings.Repeat("v", i)); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := r.Get("analysis")
	if len(a.History) != MaxHistory {
		t.Fatalf("history = %d versions, want %d", len(a.History), MaxHistory)
	}
	if a.History[0].Prompt != strings.Repeat("v", MaxHistory+2) {
		t.Errorf("most recent previous version = %q", a.History[0].Prompt)
	}
}

func TestRegistry_ResetRestoresDefault(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	r.SetPrompt(ctx, "analysis", "Perso.")
	if err := r.Reset(ctx, "analysis"); err != nil {
		t.Fatal(err)
	}
	a, _ := r.Get("analysis")
	// Historique : "Perso.", puis le défaut qu'il avait remplacé.
	if a.Custom || a.Prompt != "Analyse le ticket." || len(a.History) != 2 || a.History[0].Prompt != "Perso." || a.History[1].Prompt != "Analyse le ticket." {
		t.Errorf("after reset = %+v", a)
	}
}

// Un prompt invalide est refusé avec une raison, et l'ancien reste en
// vigueur : un repère manquant ferait perdre au modèle les données du
// document (types candidats, description...).
func TestRegistry_InvalidPromptsAreRefused(t *testing.T) {
	r, _ := newTestRegistry(t)
	cases := map[string]string{
		"vide":            "   ",
		"repère manquant": "Choisis un type.",
		"repère inconnu":  "Types : {{types}} {{type}}",
	}
	for name, prompt := range cases {
		err := r.SetPrompt(context.Background(), "classification", prompt)
		if err == nil {
			t.Errorf("%s: SetPrompt(%q) = nil, want a refusal", name, prompt)
		}
	}
	if got := r.Prompt("classification"); !strings.Contains(got, "{{types}}") {
		t.Errorf("prompt changed after refusals: %q", got)
	}
	if err := r.SetPrompt(context.Background(), "inconnu", "x"); err == nil {
		t.Error("unknown agent accepted")
	}
}

// Store en panne à l'écriture : erreur remontée, rien ne change en mémoire.
func TestRegistry_StoreFailureChangesNothing(t *testing.T) {
	r, store := newTestRegistry(t)
	store.Err = errors.New("atlas injoignable")
	if err := r.SetPrompt(context.Background(), "analysis", "Perso."); err == nil {
		t.Fatal("SetPrompt = nil, want the store error")
	}
	if r.Prompt("analysis") != "Analyse le ticket." {
		t.Error("prompt changed although not saved")
	}
}

func TestRegistry_AgentsInDefinitionOrder(t *testing.T) {
	r, _ := newTestRegistry(t)
	all := r.Agents()
	if len(all) != 2 || all[0].ID != "classification" || all[1].ID != "analysis" {
		t.Errorf("Agents() = %+v", all)
	}
}
