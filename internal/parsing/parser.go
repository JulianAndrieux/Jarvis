package parsing

import (
	"context"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

// defaultDPI est la résolution de rendu par défaut, imposée par le brief
// (rendu page → PNG 200 DPI).
const defaultDPI = 200

// PageResult est le résultat de l'étage Parsing pour une page.
//
// Conformément à la stratégie d'échec retenue (échec immédiat, pas de
// retry, marquage pour revue humaine — voir CLAUDE.md), un échec de rendu
// ou d'appel VLM sur une page ne fait jamais échouer tout le document :
// la page est marquée Failed et le traitement continue sur les pages
// suivantes.
type PageResult struct {
	Page     int
	Markdown string
	Model    vlm.ModelInfo
	Prompt   string
	Failed   bool
	Error    string
}

// Parser orchestre le rendu PNG puis l'appel au VLM pour chaque page
// demandée.
type Parser struct {
	Renderer Renderer
	VLM      vlm.Client
	// DPI est la résolution de rendu ; 0 retombe sur defaultDPI (200).
	DPI int
}

// ParsePages traite chaque page de pages dans l'ordre. Une erreur de
// niveau document (typiquement : contexte annulé) interrompt le traitement
// et est retournée ; les échecs par page (rendu ou VLM) sont capturés dans
// PageResult.Failed/Error sans interrompre les autres pages.
func (p Parser) ParsePages(ctx context.Context, path string, pages []int) ([]PageResult, error) {
	dpi := p.DPI
	if dpi == 0 {
		dpi = defaultDPI
	}

	results := make([]PageResult, 0, len(pages))
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("parsing: %s: %w", path, err)
		}

		results = append(results, p.parseOnePage(ctx, path, page, dpi))
	}
	return results, nil
}

func (p Parser) parseOnePage(ctx context.Context, path string, page, dpi int) PageResult {
	png, err := p.Renderer.RenderPage(ctx, path, page, dpi)
	if err != nil {
		return PageResult{Page: page, Failed: true, Error: fmt.Sprintf("render: %v", err)}
	}

	out, err := p.VLM.ParsePage(ctx, vlm.PageImage{Page: page, PNG: png})
	if err != nil {
		return PageResult{Page: page, Failed: true, Error: fmt.Sprintf("vlm: %v", err)}
	}

	return PageResult{
		Page:     page,
		Markdown: out.Markdown,
		Model:    out.Model,
		Prompt:   out.Prompt,
	}
}
