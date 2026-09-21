package doctype

import "testing"

func TestNewDefaultRegistry_HasDevis(t *testing.T) {
	r := NewDefaultRegistry()

	reg, ok := r.Get("devis")
	if !ok {
		t.Fatal(`Get("devis") ok = false, want true`)
	}
	if reg.Description == "" {
		t.Error("devis registration has no description")
	}
}

func TestDevis_SchemaDerivesWithoutError(t *testing.T) {
	r := NewDefaultRegistry()
	reg, _ := r.Get("devis")

	s, err := reg.Schema()
	if err != nil {
		t.Fatalf("Schema() error = %v, want nil", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Schema()[\"properties\"] is not a map: %#v", s["properties"])
	}
	for _, want := range []string{"numero", "fournisseur", "montant_total", "date_validite"} {
		if _, ok := props[want]; !ok {
			t.Errorf("properties missing %q, got %v", want, props)
		}
	}
}
