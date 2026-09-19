package extraction

import (
	"encoding/json"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

// MergedResult est la fusion des extractions de toutes les pages d'un
// document en un seul enregistrement. Répond à une vraie limite observée
// de la granularité "un JSON par page" (jalon 1) : un document dont les
// champs sont répartis sur plusieurs pages (ex. numéro page 1, total
// page 2) n'a, page par page, jamais l'information complète — voir
// CLAUDE.md, jalon 10 finding 3 / jalon 11.
//
// Chaque étage garde son résultat par page (Extractor.ExtractPages,
// inchangé) ; MergePages est une étape additionnelle, pas un remplacement.
type MergedResult struct {
	JSON                json.RawMessage
	Model               llm.ModelInfo
	Prompt              string
	NeedsReview         bool
	LowConfidenceFields []string
	Failed              bool
	Error               string
}

// MergePages fusionne les résultats de plusieurs pages : pour chaque
// champ schema.Field (détecté via IsFieldNode, y compris imbriqué), la
// valeur retenue est celle de la page où la confiance est la plus haute.
// Les pages en échec (Result.Failed) sont ignorées ; si toutes les pages
// ont échoué (ou qu'il n'y a aucune page), le résultat fusionné est
// lui-même marqué en échec — cohérent avec la stratégie retenue (jamais
// de valeur inventée en silence).
func MergePages(results []Result, threshold float64) MergedResult {
	threshold = ResolveConfidenceThreshold(threshold)

	var successful []Result
	for _, r := range results {
		if !r.Failed {
			successful = append(successful, r)
		}
	}
	if len(successful) == 0 {
		return MergedResult{Failed: true, Error: "aucune page n'a été extraite avec succès"}
	}
	if len(successful) == 1 {
		r := successful[0]
		lowFields, err := LowConfidenceFields(r.JSON, threshold)
		if err != nil {
			return MergedResult{Failed: true, Error: fmt.Sprintf("invalid JSON: %v", err)}
		}
		return MergedResult{
			JSON: r.JSON, Model: r.Model, Prompt: r.Prompt,
			NeedsReview: len(lowFields) > 0, LowConfidenceFields: lowFields,
		}
	}

	decoded := make([]map[string]any, len(successful))
	for i, r := range successful {
		var m map[string]any
		if err := json.Unmarshal(r.JSON, &m); err != nil {
			return MergedResult{Failed: true, Error: fmt.Sprintf("invalid JSON for page %d: %v", r.Page, err)}
		}
		decoded[i] = m
	}

	merged := mergeNodes(decoded)
	out, err := json.Marshal(merged)
	if err != nil {
		return MergedResult{Failed: true, Error: fmt.Sprintf("marshal merged JSON: %v", err)}
	}

	lowFields, err := LowConfidenceFields(out, threshold)
	if err != nil {
		return MergedResult{Failed: true, Error: fmt.Sprintf("invalid merged JSON: %v", err)}
	}

	first := successful[0]
	return MergedResult{
		JSON: out, Model: first.Model, Prompt: first.Prompt,
		NeedsReview: len(lowFields) > 0, LowConfidenceFields: lowFields,
	}
}

// mergeNodes fusionne N objets décodés ayant la même forme (même schéma :
// toutes les pages d'un document sont extraites contre le même type de
// document). Pour chaque clé, mergeFieldValues choisit la meilleure valeur.
func mergeNodes(nodes []map[string]any) map[string]any {
	if len(nodes) == 0 {
		return map[string]any{}
	}
	result := make(map[string]any, len(nodes[0]))
	for key := range nodes[0] {
		values := make([]any, len(nodes))
		for i, n := range nodes {
			values[i] = n[key]
		}
		result[key] = mergeFieldValues(values)
	}
	return result
}

// mergeFieldValues fusionne la même clé à travers plusieurs pages :
//   - si toutes les valeurs sont des schema.Field (value/confidence/
//     source_snippet), garde celle à la plus haute confiance ;
//   - sinon, si toutes sont des objets imbriqués, fusionne récursivement ;
//   - sinon (tableaux, types simples...), garde la première valeur non nil
//     — pas de fusion plus fine tant qu'un vrai besoin ne s'est pas montré.
func mergeFieldValues(values []any) any {
	allFieldNodes := true
	var best any
	bestConfidence := -1.0
	for _, v := range values {
		m, ok := v.(map[string]any)
		if !ok || !IsFieldNode(m) {
			allFieldNodes = false
			break
		}
		confidence, _ := m["confidence"].(float64)
		if confidence > bestConfidence {
			bestConfidence = confidence
			best = v
		}
	}
	if allFieldNodes {
		return best
	}

	allMaps := true
	maps := make([]map[string]any, 0, len(values))
	for _, v := range values {
		m, ok := v.(map[string]any)
		if !ok {
			allMaps = false
			break
		}
		maps = append(maps, m)
	}
	if allMaps {
		return mergeNodes(maps)
	}

	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return values[0]
}
