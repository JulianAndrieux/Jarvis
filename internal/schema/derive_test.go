package schema

import (
	"reflect"
	"testing"
)

func TestDerive_FieldString(t *testing.T) {
	got, err := Derive(reflect.TypeOf(Field[string]{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":          map[string]any{"type": "string"},
			"confidence":     map[string]any{"type": "number", "minimum": 0.0, "maximum": 1.0},
			"source_snippet": map[string]any{"type": "string"},
		},
		"required": []any{"value", "confidence", "source_snippet"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive() = %#v, want %#v", got, want)
	}
}

func TestDerive_FieldFloat64(t *testing.T) {
	got, err := Derive(reflect.TypeOf(Field[float64]{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	props := got["properties"].(map[string]any)
	if !reflect.DeepEqual(props["value"], map[string]any{"type": "number"}) {
		t.Errorf("value schema = %#v, want number", props["value"])
	}
}

func TestDerive_FieldInt(t *testing.T) {
	got, err := Derive(reflect.TypeOf(Field[int]{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	props := got["properties"].(map[string]any)
	if !reflect.DeepEqual(props["value"], map[string]any{"type": "integer"}) {
		t.Errorf("value schema = %#v, want integer", props["value"])
	}
}

func TestDerive_FieldBool(t *testing.T) {
	got, err := Derive(reflect.TypeOf(Field[bool]{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	props := got["properties"].(map[string]any)
	if !reflect.DeepEqual(props["value"], map[string]any{"type": "boolean"}) {
		t.Errorf("value schema = %#v, want boolean", props["value"])
	}
}

type sampleDoc struct {
	Numero      Field[string]  `json:"numero" desc:"Numéro de la facture"`
	Total       Field[float64] `json:"total_ttc"`
	unexported  string
	Ignored     string         `json:"-"`
	OptionalRef *Field[string] `json:"reference,omitempty"`
}

func TestDerive_StructOfFields(t *testing.T) {
	got, err := Derive(reflect.TypeOf(sampleDoc{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}

	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties is not a map: %#v", got["properties"])
	}

	if _, ok := props["numero"]; !ok {
		t.Error("properties missing 'numero'")
	}
	if _, ok := props["total_ttc"]; !ok {
		t.Error("properties missing 'total_ttc'")
	}
	if _, ok := props["unexported"]; ok {
		t.Error("properties should not contain unexported fields")
	}
	if _, ok := props["Ignored"]; ok {
		t.Error(`properties should not contain json:"-" fields`)
	}
	if _, ok := props["reference"]; !ok {
		t.Error("properties missing 'reference' (from a pointer field)")
	}

	numeroSchema, ok := props["numero"].(map[string]any)
	if !ok {
		t.Fatalf("numero schema is not a map: %#v", props["numero"])
	}
	if numeroSchema["description"] != "Numéro de la facture" {
		t.Errorf("numero description = %v, want %q", numeroSchema["description"], "Numéro de la facture")
	}

	required, ok := got["required"].([]any)
	if !ok {
		t.Fatalf("required is not a slice: %#v", got["required"])
	}
	wantRequired := map[string]bool{"numero": true, "total_ttc": true}
	gotRequired := map[string]bool{}
	for _, r := range required {
		gotRequired[r.(string)] = true
	}
	for name := range wantRequired {
		if !gotRequired[name] {
			t.Errorf("required = %v, want it to contain %q", required, name)
		}
	}
	// Le champ pointeur (optionnel) ne doit pas être dans required.
	if gotRequired["reference"] {
		t.Errorf("required = %v, want it to NOT contain 'reference' (pointer = optional)", required)
	}
}

func TestDerive_Slice(t *testing.T) {
	type withList struct {
		Lignes []Field[string] `json:"lignes"`
	}

	got, err := Derive(reflect.TypeOf(withList{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	props := got["properties"].(map[string]any)
	lignesSchema, ok := props["lignes"].(map[string]any)
	if !ok {
		t.Fatalf("lignes schema is not a map: %#v", props["lignes"])
	}
	if lignesSchema["type"] != "array" {
		t.Errorf("lignes type = %v, want array", lignesSchema["type"])
	}
	items, ok := lignesSchema["items"].(map[string]any)
	if !ok {
		t.Fatalf("lignes.items is not a map: %#v", lignesSchema["items"])
	}
	itemProps := items["properties"].(map[string]any)
	if !reflect.DeepEqual(itemProps["value"], map[string]any{"type": "string"}) {
		t.Errorf("lignes.items.properties.value = %#v, want string schema", itemProps["value"])
	}
}

func TestDerive_NestedPlainStruct(t *testing.T) {
	type adresse struct {
		Ville Field[string] `json:"ville"`
	}
	type withNested struct {
		Adresse adresse `json:"adresse"`
	}

	got, err := Derive(reflect.TypeOf(withNested{}))
	if err != nil {
		t.Fatalf("Derive() error = %v, want nil", err)
	}

	props := got["properties"].(map[string]any)
	adresseSchema, ok := props["adresse"].(map[string]any)
	if !ok {
		t.Fatalf("adresse schema is not a map: %#v", props["adresse"])
	}
	if adresseSchema["type"] != "object" {
		t.Errorf("adresse type = %v, want object", adresseSchema["type"])
	}
}

func TestDerive_NonStructTopLevel_ReturnsError(t *testing.T) {
	_, err := Derive(reflect.TypeOf(42))
	if err == nil {
		t.Fatal("Derive() error = nil, want non-nil for a non-struct top-level type")
	}
}

func TestDerive_UnsupportedKind_ReturnsError(t *testing.T) {
	type bad struct {
		C chan int `json:"c"`
	}
	_, err := Derive(reflect.TypeOf(bad{}))
	if err == nil {
		t.Fatal("Derive() error = nil, want non-nil for an unsupported field kind (chan)")
	}
}
