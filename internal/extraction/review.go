// Package extraction implémente l'étage 3 du pipeline : à partir du texte
// d'une page (natif ou Markdown produit par le VLM) et d'un type de
// document enregistré (internal/doctype), appeler le LLM d'extraction
// (internal/llm) avec le JSON Schema dérivé, puis vérifier la confiance de
// chaque valeur retournée.
package extraction

import (
	"encoding/json"
	"fmt"
)

// LowConfidenceFields décode raw (le JSON produit par l'étage Extraction,
// censé être conforme au schéma dérivé de internal/schema) et retourne le
// chemin de chaque champ schema.Field (objet {value, confidence,
// source_snippet}) dont la confiance est strictement inférieure à
// threshold. Ces champs doivent être marqués pour revue humaine, jamais
// acceptés silencieusement.
//
// Simplification assumée : un objet reconnu comme un champ Field (il a les
// trois clés value/confidence/source_snippet) est traité comme une feuille
// — on ne descend pas dans sa "value" pour y chercher d'éventuels champs
// imbriqués. Les types de documents actuels n'imbriquent pas de Field dans
// la Value d'un autre Field ; si un futur type le fait, cette fonction
// devra être étendue en conséquence (pas un détail caché : à signaler).
func LowConfidenceFields(raw json.RawMessage, threshold float64) ([]string, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("extraction: decode JSON: %w", err)
	}

	var low []string
	walkFields("", decoded, threshold, &low)
	return low, nil
}

func walkFields(path string, node any, threshold float64, low *[]string) {
	switch v := node.(type) {
	case map[string]any:
		if IsFieldNode(v) {
			confidence, _ := v["confidence"].(float64)
			if confidence < threshold {
				*low = append(*low, path)
			}
			return
		}
		for key, child := range v {
			walkFields(joinPath(path, key), child, threshold, low)
		}
	case []any:
		for i, child := range v {
			walkFields(fmt.Sprintf("%s[%d]", path, i), child, threshold, low)
		}
	}
}

// IsFieldNode signale si m est un objet JSON au format schema.Field
// (value/confidence/source_snippet). Exporté pour être réutilisé par
// d'autres consommateurs du JSON produit par l'étage Extraction (ex.
// l'affichage web), sans dupliquer cette détection.
func IsFieldNode(m map[string]any) bool {
	_, hasValue := m["value"]
	_, hasConfidence := m["confidence"]
	_, hasSnippet := m["source_snippet"]
	return hasValue && hasConfidence && hasSnippet
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
