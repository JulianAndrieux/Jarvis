package formats

import "context"

// Rendition est ce que la conversion produit pour un fichier : une
// version PDF (aperçu fidèle, miniature, entrée du pipeline) et, pour
// certaines familles, un aperçu natif plus lisible que le PDF (feuilles
// de calcul en HTML, image HEIC/TIFF en JPEG).
type Rendition struct {
	PDF         []byte
	Preview     []byte // nil si la famille n'en a pas
	PreviewMIME string
}

// Converter produit la Rendition d'un fichier. srcPath garde l'extension
// d'origine : LibreOffice s'en sert pour choisir son filtre d'import.
// Port : LocalConverter en production (LibreOffice, sips), une fake dans
// les tests — aucun test unitaire ne lance LibreOffice.
type Converter interface {
	Convert(ctx context.Context, f Format, srcPath string) (Rendition, error)
}
