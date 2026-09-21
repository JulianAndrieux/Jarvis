package doctype

import "testing"

func TestNewDefaultRegistry_HasPieceIdentite(t *testing.T) {
	r := NewDefaultRegistry()

	reg, ok := r.Get("piece_identite")
	if !ok {
		t.Fatal(`Get("piece_identite") ok = false, want true`)
	}
	if reg.Description == "" {
		t.Error("piece_identite registration has no description")
	}
}

func TestPieceIdentite_SchemaDerivesWithoutError(t *testing.T) {
	r := NewDefaultRegistry()
	reg, _ := r.Get("piece_identite")

	s, err := reg.Schema()
	if err != nil {
		t.Fatalf("Schema() error = %v, want nil", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Schema()[\"properties\"] is not a map: %#v", s["properties"])
	}
	for _, want := range []string{"nom", "prenom", "date_naissance", "numero_document", "date_expiration"} {
		if _, ok := props[want]; !ok {
			t.Errorf("properties missing %q, got %v", want, props)
		}
	}
}
