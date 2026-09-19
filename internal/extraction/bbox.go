package extraction

import (
	"encoding/json"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
)

// AttachBBoxes enrichit raw (JSON produit par l'étage Extraction, conforme
// au schéma dérivé de internal/schema) en ajoutant une clé "bbox" à chaque
// champ schema.Field dont le source_snippet a pu être localisé parmi
// words. Un champ dont le snippet n'est pas trouvé reste inchangé (pas de
// bbox inventé).
//
// C'est un enrichissement, pas une ré-extraction : la présence/absence de
// "bbox" ne change jamais Value/Confidence/SourceSnippet.
func AttachBBoxes(raw json.RawMessage, words []bbox.Word) (json.RawMessage, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("extraction: decode JSON: %w", err)
	}

	attachBBoxesWalk(decoded, words)

	out, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("extraction: marshal bbox-enriched JSON: %w", err)
	}
	return out, nil
}

func attachBBoxesWalk(node any, words []bbox.Word) {
	switch v := node.(type) {
	case map[string]any:
		if IsFieldNode(v) {
			snippet, _ := v["source_snippet"].(string)
			if snippet == "" {
				return
			}
			if b, ok := bbox.FindSnippetBBox(words, snippet); ok {
				v["bbox"] = b
			}
			return
		}
		for _, child := range v {
			attachBBoxesWalk(child, words)
		}
	case []any:
		for _, child := range v {
			attachBBoxesWalk(child, words)
		}
	}
}
