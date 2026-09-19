// Package parsing implémente l'étage 2 du pipeline : pour les pages sans
// texte fiable (cf. internal/triage), rendre la page en PNG puis appeler le
// VLM de document (internal/vlm) pour obtenir du Markdown/HTML fidèle au
// layout.
package parsing

import "context"

// Renderer rend une page de PDF en PNG. C'est un port : la production
// s'appuie sur pdftoppm (PdftoppmRenderer), les tests unitaires sur
// FakeRenderer. Aucune implémentation ne doit nécessiter de GPU.
type Renderer interface {
	// RenderPage rend la page (1-indexée) de path en PNG, à la résolution
	// dpi.
	RenderPage(ctx context.Context, path string, page int, dpi int) ([]byte, error)
}
