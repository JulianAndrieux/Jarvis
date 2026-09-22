//go:build integration

package formats

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Conversion réelle (LibreOffice, sips) de chaque fichier du corpus
// testdata/formats — un par format géré (scripts/gen_format_fixtures.sh).
// Nécessite soffice, sips, pdftotext et pdfinfo sur le PATH.

func fixture(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "formats", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s missing (run scripts/gen_format_fixtures.sh): %v", name, err)
	}
	return path
}

func pdfText(t *testing.T, pdf []byte) string {
	t.Helper()
	cmd := exec.Command("pdftotext", "-", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	return string(out)
}

func pdfPageSize(t *testing.T, pdf []byte) string {
	t.Helper()
	cmd := exec.Command("pdfinfo", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pdfinfo: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Page size:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Page size:"))
		}
	}
	return ""
}

func TestLocalConverter_EveryFormatProducesAReadablePDF(t *testing.T) {
	c := &LocalConverter{ProfileDir: t.TempDir()}
	cases := []struct {
		file string
		want []string // textes attendus dans le PDF
	}{
		{"facture.docx", []string{"FAC-2026-0917", "Atelier Dubois", "2 317,20"}},
		{"facture.doc", []string{"FAC-2026-0917", "Atelier Dubois"}},
		{"facture.odt", []string{"FAC-2026-0917", "Atelier Dubois"}},
		{"facture.rtf", []string{"FAC-2026-0917", "Atelier Dubois"}},
		{"presentation.pptx", []string{"FAC-2026-0917"}},
		{"presentation.ppt", []string{"FAC-2026-0917"}},
		{"presentation.odp", []string{"FAC-2026-0917"}},
		{"releve.xlsx", []string{"Loyer septembre", "Libellé"}},
		{"releve.xls", []string{"Loyer septembre", "Libellé"}},
		{"releve.ods", []string{"Loyer septembre", "Libellé"}},
		{"releve.csv", []string{"Loyer septembre", "Libellé", "-1 250,00"}}, // Windows-1252, points-virgules
		{"notes.txt", []string{"Compte rendu de chantier", "DEV-2026-044"}},
		{"courses.md", []string{"Liste de courses", "œufs"}},
		{"donnees.json", []string{"FAC-2026-0917", "2317.20"}},
		{"page.html", []string{"FAC-2026-0917", "Atelier Dubois"}},
		{"message.eml", []string{"Atelier Dubois", "FAC-2026-0917", "Pièces jointes", "FAC-2026-0917.pdf"}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			src := fixture(t, tc.file)
			f := Detect(tc.file, nil)
			r, err := c.Convert(context.Background(), f, src)
			if err != nil {
				t.Fatalf("Convert(%s) error = %v", tc.file, err)
			}
			if !bytes.HasPrefix(r.PDF, []byte("%PDF-")) {
				t.Fatalf("Convert(%s) did not produce a PDF (%d bytes)", tc.file, len(r.PDF))
			}
			text := pdfText(t, r.PDF)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("PDF of %s does not contain %q; text:\n%s", tc.file, want, text)
				}
			}
		})
	}
}

func TestLocalConverter_SheetsGetAnHTMLPreviewOfTheirCells(t *testing.T) {
	c := &LocalConverter{ProfileDir: t.TempDir()}
	for _, file := range []string{"releve.xlsx", "releve.xls", "releve.ods"} {
		r, err := c.Convert(context.Background(), Detect(file, nil), fixture(t, file))
		if err != nil {
			t.Fatalf("Convert(%s) error = %v", file, err)
		}
		if r.PreviewMIME != "text/html" || !strings.Contains(string(r.Preview), "Loyer septembre") {
			t.Errorf("%s preview = %s (%d bytes), want HTML with the cells", file, r.PreviewMIME, len(r.Preview))
		}
	}
}

// Les images deviennent une page A4 sans couche texte : le pipeline les
// enverra au VLM comme un scan. HEIC/TIFF ont en plus un aperçu JPEG.
func TestLocalConverter_ImagesBecomeA4ScansWithPreviewWhenNeeded(t *testing.T) {
	c := &LocalConverter{ProfileDir: t.TempDir()}
	for _, tc := range []struct {
		file        string
		wantPreview bool
	}{
		{"scan.jpg", false}, {"scan.png", false}, {"scan.heic", true}, {"scan.tiff", true},
	} {
		r, err := c.Convert(context.Background(), Detect(tc.file, nil), fixture(t, tc.file))
		if err != nil {
			t.Fatalf("Convert(%s) error = %v", tc.file, err)
		}
		if strings.TrimSpace(pdfText(t, r.PDF)) != "" {
			t.Errorf("%s: PDF has a text layer, want an image-only page (VLM path)", tc.file)
		}
		// 827x1170 px normalisé à 205 DPI : ~290 x 411 pt ; une photo de
		// 2400 px de haut donnerait ~843 pt (A4). Le point vérifié : pas
		// une page géante à 72 DPI (1170 px -> 1170 pt).
		if size := pdfPageSize(t, r.PDF); strings.Contains(size, "1170") {
			t.Errorf("%s: page size %s, want the image normalised (not 1 px = 1 pt)", tc.file, size)
		}
		if gotPreview := r.PreviewMIME == "image/jpeg" && len(r.Preview) > 0; gotPreview != tc.wantPreview {
			t.Errorf("%s: JPEG preview = %v, want %v", tc.file, gotPreview, tc.wantPreview)
		}
	}
}

// Un docx tronqué : LibreOffice écrit "source file could not be loaded"
// mais sort en 0 — c'est l'absence de fichier produit qui doit devenir
// une erreur explicite (sinon le job "réussirait" avec un PDF vide).
func TestLocalConverter_CorruptFileFailsWithAClearError(t *testing.T) {
	c := &LocalConverter{ProfileDir: t.TempDir()}
	whole, err := os.ReadFile(fixture(t, "facture.docx"))
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "casse.docx")
	if err := os.WriteFile(bad, whole[:900], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = c.Convert(context.Background(), Detect("casse.docx", nil), bad)
	if err == nil {
		t.Fatal("Convert(truncated docx) error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "libreoffice") || !strings.Contains(err.Error(), "could not be loaded") {
		t.Errorf("error = %v, want it to name libreoffice and carry its message", err)
	}
}
