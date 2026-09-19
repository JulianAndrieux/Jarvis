package pipeline

import (
	"context"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
)

// attachBBoxes enrichit les résultats d'extraction des pages dont le
// texte vient du triage (SourceNative) avec la position de chaque valeur,
// via p.BBox. C'est un enrichissement best-effort : toute erreur (bbox
// extractor indisponible, snippet introuvable) laisse le JSON tel quel
// plutôt que de faire échouer le pipeline — la valeur extraite et sa
// confiance restent la source de vérité, le bbox n'est qu'un bonus de
// provenance.
func (p Pipeline) attachBBoxes(ctx context.Context, path string, merged []PageContent, results []extraction.Result) []extraction.Result {
	nativePages := make(map[int]bool)
	for _, m := range merged {
		if m.Source == SourceNative {
			nativePages[m.Page] = true
		}
	}
	if len(nativePages) == 0 {
		return results
	}

	pageWords, err := p.BBox.ExtractWords(ctx, path)
	if err != nil {
		return results
	}
	wordsByPage := make(map[int][]bbox.Word, len(pageWords))
	for _, pw := range pageWords {
		wordsByPage[pw.Page] = pw.Words
	}

	enriched := make([]extraction.Result, len(results))
	copy(enriched, results)
	for i, r := range enriched {
		if !nativePages[r.Page] {
			continue
		}
		words, ok := wordsByPage[r.Page]
		if !ok || len(words) == 0 {
			continue
		}
		augmented, err := extraction.AttachBBoxes(r.JSON, words)
		if err != nil {
			continue
		}
		enriched[i].JSON = augmented
	}
	return enriched
}
