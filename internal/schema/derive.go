package schema

import (
	"fmt"
	"reflect"
	"strings"
)

// Derive construit un JSON Schema (en tant que map[string]any, prêt à être
// marshalé) à partir d'un struct Go. C'est une fonction pure basée sur la
// réflexion : aucune I/O, testable sans LLM.
//
// Règles :
//   - Un champ struct/pointer/slice est dérivé récursivement.
//   - Field[T] (voir field.go) est reconnu structurellement (trois champs
//     Value/Confidence/SourceSnippet) et rendu comme l'objet
//     {value, confidence, source_snippet} attendu par l'étage Extraction.
//   - Le nom de propriété JSON vient du tag `json:"..."` (partie avant la
//     virgule) ; `json:"-"` exclut le champ ; sans tag, le nom du champ Go
//     est utilisé tel quel.
//   - Le tag `desc:"..."` ajoute "description" au schéma du champ.
//   - Un champ pointeur est optionnel (absent de "required") ; son schéma
//     est celui du type pointé, dérivé directement (pas de double
//     enveloppe).
//   - Les champs non-exportés sont ignorés.
func Derive(t reflect.Type) (map[string]any, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema: Derive requires a struct type, got %s", t.Kind())
	}
	return deriveAny(t)
}

// deriveAny dérive le schéma d'un type quelconque (pas nécessairement un
// struct de premier niveau) — utilisé pour la récursion sur les champs,
// éléments de slice, etc.
func deriveAny(t reflect.Type) (map[string]any, error) {
	if isFieldType(t) {
		return deriveFieldType(t)
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Struct:
		return deriveStruct(t)
	case reflect.Slice, reflect.Array:
		items, err := deriveAny(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Pointer:
		return deriveAny(t.Elem())
	default:
		return nil, fmt.Errorf("schema: unsupported field kind %s (%s)", t.Kind(), t)
	}
}

// isFieldType reconnaît structurellement Field[T] : un struct avec
// exactement les champs Value, Confidence (float64) et SourceSnippet
// (string). Reconnaissance structurelle plutôt que par nom de type, pour
// ne pas dépendre de la façon dont reflect nomme les instanciations
// génériques.
func isFieldType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() != 3 {
		return false
	}
	_, hasValue := t.FieldByName("Value")
	confidence, hasConfidence := t.FieldByName("Confidence")
	sourceSnippet, hasSourceSnippet := t.FieldByName("SourceSnippet")
	if !hasValue || !hasConfidence || !hasSourceSnippet {
		return false
	}
	return confidence.Type.Kind() == reflect.Float64 && sourceSnippet.Type.Kind() == reflect.String
}

func deriveFieldType(t reflect.Type) (map[string]any, error) {
	valueField, _ := t.FieldByName("Value")
	valueSchema, err := deriveAny(valueField.Type)
	if err != nil {
		return nil, fmt.Errorf("schema: derive Field value type: %w", err)
	}

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":          valueSchema,
			"confidence":     map[string]any{"type": "number", "minimum": 0.0, "maximum": 1.0},
			"source_snippet": map[string]any{"type": "string"},
		},
		"required": []any{"value", "confidence", "source_snippet"},
	}, nil
}

func deriveStruct(t reflect.Type) (map[string]any, error) {
	properties := map[string]any{}
	var required []any

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		name, description, skip := parseFieldTags(f)
		if skip {
			continue
		}

		fieldSchema, err := deriveAny(f.Type)
		if err != nil {
			return nil, fmt.Errorf("schema: field %s: %w", f.Name, err)
		}
		if description != "" {
			fieldSchema["description"] = description
		}
		properties[name] = fieldSchema

		if f.Type.Kind() != reflect.Pointer {
			required = append(required, name)
		}
	}

	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}, nil
}

// parseFieldTags lit les tags `json` et `desc` d'un champ. json:"-" donne
// skip=true. Sans tag json, le nom du champ Go est utilisé.
func parseFieldTags(f reflect.StructField) (name, description string, skip bool) {
	name = f.Name
	if jsonTag, ok := f.Tag.Lookup("json"); ok {
		parts := strings.Split(jsonTag, ",")
		if parts[0] == "-" {
			return "", "", true
		}
		if parts[0] != "" {
			name = parts[0]
		}
	}
	description = f.Tag.Get("desc")
	return name, description, false
}
