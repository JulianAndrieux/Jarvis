// Package vlm définit le port vers le modèle vision-langage de document
// utilisé par l'étage Parsing : rendre une page en Markdown/HTML fidèle au
// layout. Aucune implémentation de ce package ne doit nécessiter de GPU
// dans ses tests — voir FakeClient pour les tests, et le futur
// HTTPClient (jalon 3) pour la production (llama.cpp server).
package vlm

import "context"

// PageImage est le rendu PNG d'une page de PDF (200 DPI par défaut),
// soumis au VLM.
type PageImage struct {
	Page int
	PNG  []byte
}

// ModelInfo identifie le modèle ayant produit un résultat, pour la
// reproductibilité (nom + version, à journaliser avec chaque résultat).
type ModelInfo struct {
	Name    string
	Version string
}

// ParseResult est la sortie du VLM pour une page : le Markdown/HTML fidèle
// au layout, plus la provenance (modèle et prompt utilisés).
type ParseResult struct {
	Markdown string
	Model    ModelInfo
	Prompt   string
}

// Client est le port vers le VLM de document. Une implémentation HTTP
// (llama.cpp server, API compatible OpenAI) arrivera au jalon 3.
type Client interface {
	ParsePage(ctx context.Context, img PageImage) (ParseResult, error)
}
