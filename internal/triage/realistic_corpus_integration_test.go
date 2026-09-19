//go:build integration

// Corpus de factures synthétiques plus denses que native.pdf/scanned.pdf
// (mise en page proche d'une vraie facture, tableau de lignes, montants
// ambigus, formats numériques, document multi-pages, scan légèrement
// pivoté) — voir scripts/gen_fixtures.py. Ces tests valident que le
// triage classe correctement chaque document ; la justesse de
// l'extraction (LLM) sur ce même corpus est validée manuellement contre
// de vrais serveurs locaux et documentée dans CLAUDE.md, pas ici (aucun
// test ne doit avoir besoin d'un GPU).
package triage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

func TestRealisticCorpus_NativeDocuments_HaveTextLayer(t *testing.T) {
	cases := []struct {
		name      string
		file      string
		wantPages int
	}{
		{"facture_multiligne", "facture_multiligne.pdf", 1},
		{"facture_multipage", "facture_multipage.pdf", 2},
		{"facture_ambigue", "facture_ambigue.pdf", 1},
		{"facture_format_europeen", "facture_format_europeen.pdf", 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Detector{Extractor: PdftotextExtractor{}, Thresholds: DefaultThresholds()}

			got, err := d.Detect(context.Background(), fixturePath(t, c.file))
			if err != nil {
				t.Fatalf("Detect() error = %v, want nil", err)
			}
			if !got.HasTextLayer {
				t.Errorf("HasTextLayer = false, want true (score=%.2f, reasons=%v)", got.Score, got.Reasons)
			}
			if len(got.Pages) != c.wantPages {
				t.Errorf("len(Pages) = %d, want %d", len(got.Pages), c.wantPages)
			}
		})
	}
}

func TestRealisticCorpus_ScannedDocument_HasNoTextLayer(t *testing.T) {
	d := Detector{Extractor: PdftotextExtractor{}, Thresholds: DefaultThresholds()}

	got, err := d.Detect(context.Background(), fixturePath(t, "facture_scannee_realiste.pdf"))
	if err != nil {
		t.Fatalf("Detect() error = %v, want nil", err)
	}
	if got.HasTextLayer {
		t.Errorf("HasTextLayer = true, want false for a scanned (image-only) page — score=%.2f", got.Score)
	}
}

func TestRealisticCorpus_Multipage_FieldsSplitAcrossPages(t *testing.T) {
	// Documente une vraie limite architecturale : le numéro/fournisseur
	// sont sur la page 1, le total sur la page 2, et rien ne les relie.
	// Ce test ne vérifie pas l'extraction (qui a besoin d'un LLM) mais
	// confirme la prémisse : chaque page, prise isolément, n'a bien
	// qu'une partie de l'information attendue par le schéma Facture.
	e := PdftotextExtractor{}
	pages, err := e.ExtractPerPage(context.Background(), fixturePath(t, "facture_multipage.pdf"))
	if err != nil {
		t.Fatalf("ExtractPerPage() error = %v, want nil", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d, want 2", len(pages))
	}

	page1HasNumero := strings.Contains(pages[0].Text, "2026-0271")
	page1HasTotal := strings.Contains(pages[0].Text, "Total TTC")
	page2HasNumero := strings.Contains(pages[1].Text, "2026-0271")
	page2HasTotal := strings.Contains(pages[1].Text, "Total TTC")

	if !page1HasNumero {
		t.Error("page 1 ne contient pas le numéro de facture, la fixture ne teste plus ce qu'elle est censée tester")
	}
	if page1HasTotal {
		t.Error("page 1 contient le total — la fixture ne sépare plus les champs entre pages")
	}
	if page2HasNumero {
		t.Error("page 2 contient le numéro — la fixture ne sépare plus les champs entre pages")
	}
	if !page2HasTotal {
		t.Error("page 2 ne contient pas le total, la fixture ne teste plus ce qu'elle est censée tester")
	}
}
