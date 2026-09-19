// Package bbox extrait la position (bounding box) des mots d'un PDF, pour
// compléter la provenance des valeurs extraites (page + bbox + extrait
// source, cf. brief). Ne couvre aujourd'hui que les pages avec une couche
// texte native (via pdftotext -bbox) — voir CLAUDE.md pour la stratégie
// sur les pages passées par le VLM.
package bbox

import "context"

// Word est un mot positionné sur une page, en points PDF (même repère que
// le MediaBox : origine en bas à gauche, indépendant de tout DPI de
// rendu).
type Word struct {
	Text                   string
	XMin, YMin, XMax, YMax float64
}

// PageWords est l'ensemble des mots positionnés d'une page.
type PageWords struct {
	Page          int
	Width, Height float64
	Words         []Word
}

// Box est le rectangle englobant d'une valeur extraite, en points PDF.
type Box struct {
	XMin float64 `json:"x_min"`
	YMin float64 `json:"y_min"`
	XMax float64 `json:"x_max"`
	YMax float64 `json:"y_max"`
}

// Extractor extrait les mots positionnés de chaque page d'un PDF. Port :
// PdftotextBBoxExtractor en production, FakeExtractor pour les tests.
type Extractor interface {
	ExtractWords(ctx context.Context, path string) ([]PageWords, error)
}
