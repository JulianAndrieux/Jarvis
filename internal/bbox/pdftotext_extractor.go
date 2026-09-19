package bbox

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// PdftotextBBoxExtractor extrait les mots positionnés d'un PDF via
// `pdftotext -bbox` (poppler-utils, le même paquet que pdftotext en mode
// texte simple utilisé par internal/triage). Ne couvre que les pages avec
// une couche texte native : sur une page sans texte, la sortie ne
// contient aucun mot pour cette page (ce n'est pas une erreur).
type PdftotextBBoxExtractor struct {
	BinPath string
}

func (e PdftotextBBoxExtractor) ExtractWords(ctx context.Context, path string) ([]PageWords, error) {
	bin := e.BinPath
	if bin == "" {
		bin = "pdftotext"
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-bbox", path, "-")
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("bbox: pdftotext -bbox failed on %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("bbox: run pdftotext -bbox on %s: %w", path, err)
	}

	var doc bboxDocument
	if err := xml.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("bbox: parse pdftotext -bbox output for %s: %w", path, err)
	}

	pages := make([]PageWords, len(doc.Body.Doc.Pages))
	for i, p := range doc.Body.Doc.Pages {
		words := make([]Word, len(p.Words))
		for j, w := range p.Words {
			words[j] = Word{
				Text: strings.TrimSpace(w.Text),
				XMin: w.XMin, YMin: w.YMin, XMax: w.XMax, YMax: w.YMax,
			}
		}
		pages[i] = PageWords{Page: i + 1, Width: p.Width, Height: p.Height, Words: words}
	}
	return pages, nil
}

// Structs miroir du XML produit par `pdftotext -bbox` :
//
//	<html><body><doc>
//	  <page width="..." height="...">
//	    <word xMin="..." yMin="..." xMax="..." yMax="...">Texte</word>
//	    ...
//	  </page>
//	  ...
//	</doc></body></html>
//
// Le DOCTYPE en tête est ignoré par encoding/xml sans configuration
// particulière.
type bboxDocument struct {
	XMLName xml.Name `xml:"html"`
	Body    struct {
		Doc struct {
			Pages []bboxPage `xml:"page"`
		} `xml:"doc"`
	} `xml:"body"`
}

type bboxPage struct {
	Width  float64    `xml:"width,attr"`
	Height float64    `xml:"height,attr"`
	Words  []bboxWord `xml:"word"`
}

type bboxWord struct {
	XMin float64 `xml:"xMin,attr"`
	YMin float64 `xml:"yMin,attr"`
	XMax float64 `xml:"xMax,attr"`
	YMax float64 `xml:"yMax,attr"`
	Text string  `xml:",chardata"`
}
