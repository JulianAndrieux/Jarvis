package parsing

import (
	"context"
	"fmt"
	"sync"

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
	// Concurrency borne le nombre de pages traitées en parallèle ; 0 ou 1
	// (par défaut) = séquentiel, comportement inchangé par rapport aux
	// jalons précédents. À aligner sur les "slots" parallèles exposés par
	// le serveur VLM (llama-server annonce "n_slots = N" à son démarrage)
	// — un document multi-pages ne paie plus N fois un aller-retour
	// séquentiel si le serveur peut en traiter plusieurs à la fois
	// (jalon 21, cf. CLAUDE.md).
	Concurrency int
	// OnPage, si non-nil, est appelé dès qu'une page est terminée (succès
	// ou échec), avant la page suivante en mode séquentiel — jalon 23 :
	// enregistrer le texte OCR au fur et à mesure plutôt qu'à la fin du
	// document. Les appels sont sérialisés par le Parser, y compris en
	// mode parallèle : la fonction n'a pas à être sûre en concurrence.
	OnPage func(PageResult)
}

// ParsePages traite chaque page de pages. Une erreur de niveau document
// (typiquement : contexte annulé) interrompt le traitement et est
// retournée ; les échecs par page (rendu ou VLM) sont capturés dans
// PageResult.Failed/Error sans interrompre les autres pages. L'ordre des
// résultats correspond toujours à celui de pages, y compris en mode
// parallèle (Concurrency > 1).
func (p Parser) ParsePages(ctx context.Context, path string, pages []int) ([]PageResult, error) {
	dpi := p.DPI
	if dpi == 0 {
		dpi = defaultDPI
	}

	if p.Concurrency <= 1 {
		return p.parsePagesSequential(ctx, path, pages, dpi)
	}
	return p.parsePagesParallel(ctx, path, pages, dpi)
}

func (p Parser) parsePagesSequential(ctx context.Context, path string, pages []int, dpi int) ([]PageResult, error) {
	results := make([]PageResult, 0, len(pages))
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("parsing: %s: %w", path, err)
		}
		r := p.parseOnePage(ctx, path, page, dpi)
		results = append(results, r)
		if p.OnPage != nil {
			p.OnPage(r)
		}
	}
	return results, nil
}

// parsePagesParallel traite jusqu'à Concurrency pages simultanément. Les
// résultats sont écrits par index (pas un append concurrent) pour
// préserver l'ordre de pages malgré l'exécution parallèle.
func (p Parser) parsePagesParallel(ctx context.Context, path string, pages []int, dpi int) ([]PageResult, error) {
	results := make([]PageResult, len(pages))
	sem := make(chan struct{}, p.Concurrency)
	var wg sync.WaitGroup
	var onPageMu sync.Mutex

	for i, page := range pages {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return results, fmt.Errorf("parsing: %s: %w", path, err)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return results, fmt.Errorf("parsing: %s: %w", path, ctx.Err())
		}

		wg.Add(1)
		go func(i, page int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = p.parseOnePage(ctx, path, page, dpi)
			if p.OnPage != nil {
				onPageMu.Lock()
				p.OnPage(results[i])
				onPageMu.Unlock()
			}
		}(i, page)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return results, fmt.Errorf("parsing: %s: %w", path, err)
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
