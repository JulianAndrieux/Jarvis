package doctype

import "testing"

func TestNewDefaultRegistry_HasBonCommande(t *testing.T) {
	r := NewDefaultRegistry()

	reg, ok := r.Get("bon_commande")
	if !ok {
		t.Fatal(`Get("bon_commande") ok = false, want true`)
	}
	if reg.Description == "" {
		t.Error("bon_commande registration has no description")
	}
}

func TestBonCommande_SchemaDerivesWithoutError(t *testing.T) {
	r := NewDefaultRegistry()
	reg, _ := r.Get("bon_commande")

	s, err := reg.Schema()
	if err != nil {
		t.Fatalf("Schema() error = %v, want nil", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Schema()[\"properties\"] is not a map: %#v", s["properties"])
	}
	for _, want := range []string{"numero", "fournisseur", "client", "montant_total"} {
		if _, ok := props[want]; !ok {
			t.Errorf("properties missing %q, got %v", want, props)
		}
	}
}
