package formats

import (
	"context"
	"fmt"
	"os"

	"github.com/JulianAndrieux/Jarvis/internal/email"
	"github.com/JulianAndrieux/Jarvis/internal/textview"
)

// convertMarkup met en page un e-mail ou une page web sous forme de
// texte (en-têtes + corps + pièces jointes pour un e-mail). Jamais le
// HTML d'origine : LibreOffice irait chercher les images distantes qu'il
// référence (pixels de suivi d'un e-mail) — des données qui sortiraient
// de la machine. L'aperçu, lui, affiche le HTML sous une CSP qui bloque
// ces requêtes.
func (c *LocalConverter) convertMarkup(ctx context.Context, work string, f Format, src string) (Rendition, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return Rendition{}, fmt.Errorf("formats: read %s: %w", src, err)
	}
	var text string
	switch f.Family {
	case Email:
		msg, err := email.Parse(data)
		if err != nil {
			return Rendition{}, fmt.Errorf("formats: e-mail: %w", err)
		}
		text = msg.PlainText()
	default:
		page, _ := textview.Decode(data)
		text = email.HTMLToText(page)
	}
	pdf, err := c.textToPDF(ctx, work, text)
	return Rendition{PDF: pdf}, err
}
