package doctype

import "testing"

func TestNewDefaultRegistry_HasCorrespondance(t *testing.T) {
	r := NewDefaultRegistry()

	reg, ok := r.Get("correspondance")
	if !ok {
		t.Fatal(`Get("correspondance") ok = false, want true`)
	}
	if reg.Description == "" {
		t.Error("correspondance registration has no description")
	}
}

func TestCorrespondance_SchemaDerivesWithoutError(t *testing.T) {
	r := NewDefaultRegistry()
	reg, _ := r.Get("correspondance")

	s, err := reg.Schema()
	if err != nil {
		t.Fatalf("Schema() error = %v, want nil", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Schema()[\"properties\"] is not a map: %#v", s["properties"])
	}
	for _, want := range []string{"expediteur", "destinataire", "date", "objet"} {
		if _, ok := props[want]; !ok {
			t.Errorf("properties missing %q, got %v", want, props)
		}
	}
}
