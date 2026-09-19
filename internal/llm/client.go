// Package llm définit le port vers le modèle texte local utilisé par
// l'étage Extraction : produire, à partir d'un texte source et d'un JSON
// Schema cible, un JSON conforme (décodage contraint / guided decoding).
// Aucune implémentation de ce package ne doit nécessiter de GPU dans ses
// tests — voir FakeClient pour les tests, et le futur HTTPClient
// (jalon 5) pour la production (llama.cpp server).
package llm

import (
	"context"
	"encoding/json"
)

// ExtractRequest est la requête d'extraction pour une page : le texte
// source (texte natif ou Markdown produit par le VLM), le JSON Schema
// cible (dérivé d'un struct Go via internal/schema) et le prompt.
type ExtractRequest struct {
	Page   int
	Text   string
	Schema json.RawMessage
	Prompt string
}

// ModelInfo identifie le modèle ayant produit un résultat, pour la
// reproductibilité (nom + version, à journaliser avec chaque résultat).
type ModelInfo struct {
	Name    string
	Version string
}

// ExtractResult est la sortie du LLM pour une page : le JSON brut, censé
// être conforme à ExtractRequest.Schema, plus la provenance (modèle et
// prompt utilisés).
type ExtractResult struct {
	JSON   json.RawMessage
	Model  ModelInfo
	Prompt string
}

// Client est le port vers le LLM d'extraction. Une implémentation HTTP
// (llama.cpp server, décodage contraint par JSON Schema) arrivera au
// jalon 5.
type Client interface {
	Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error)
}
