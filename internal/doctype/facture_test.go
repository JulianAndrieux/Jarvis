package doctype

import "testing"

func TestNewDefaultRegistry_HasFacture(t *testing.T) {
	r := NewDefaultRegistry()

	reg, ok := r.Get("facture")
	if !ok {
		t.Fatal(`Get("facture") ok = false, want true`)
	}
	if reg.Description == "" {
		t.Error("facture registration has no description")
	}
}

func TestFacture_SchemaDerivesWithoutError(t *testing.T) {
	r := NewDefaultRegistry()
	reg, _ := r.Get("facture")

	s, err := reg.Schema()
	if err != nil {
		t.Fatalf("Schema() error = %v, want nil", err)
	}
	if s["type"] != "object" {
		t.Errorf(`Schema()["type"] = %v, want "object"`, s["type"])
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Schema()[\"properties\"] is not a map: %#v", s["properties"])
	}
	for _, want := range []string{"numero", "fournisseur", "total_ttc"} {
		if _, ok := props[want]; !ok {
			t.Errorf("properties missing %q, got %v", want, props)
		}
	}
}
