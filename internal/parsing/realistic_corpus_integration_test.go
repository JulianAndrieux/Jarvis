//go:build integration

package parsing

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestPdftoppmRenderer_RealisticCorpus_AllPagesRenderable(t *testing.T) {
	cases := []struct {
		file  string
		pages int
	}{
		{"facture_multiligne.pdf", 1},
		{"facture_multipage.pdf", 2},
		{"facture_ambigue.pdf", 1},
		{"facture_format_europeen.pdf", 1},
		{"facture_scannee_realiste.pdf", 1},
	}

	r := PdftoppmRenderer{}
	pngMagic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "fixtures", c.file)
			for page := 1; page <= c.pages; page++ {
				png, err := r.RenderPage(context.Background(), path, page, 150)
				if err != nil {
					t.Fatalf("RenderPage(page=%d) error = %v, want nil", page, err)
				}
				if !bytes.HasPrefix(png, pngMagic) {
					t.Errorf("RenderPage(page=%d) output does not start with the PNG magic bytes", page)
				}
			}
		})
	}
}
