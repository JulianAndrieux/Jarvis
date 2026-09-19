package triage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// TextExtractor extrait le texte brut de chaque page d'un PDF. C'est un
// port : la production s'appuie sur pdftotext (PdftotextExtractor), les
// tests unitaires sur FakeExtractor. Aucune implémentation ne doit
// nécessiter de GPU.
type TextExtractor interface {
	ExtractPerPage(ctx context.Context, path string) ([]PageText, error)
}

// FakeExtractor est une implémentation de test de TextExtractor : elle
// retourne des valeurs préconfigurées sans toucher au système de fichiers
// ni exécuter de binaire externe.
type FakeExtractor struct {
	Pages []PageText
	Err   error
}

func (f FakeExtractor) ExtractPerPage(ctx context.Context, path string) ([]PageText, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Pages, nil
}

// PdftotextExtractor extrait le texte d'un PDF en s'appuyant sur le binaire
// externe `pdftotext` (poppler-utils). BinPath permet de surcharger le nom/
// chemin du binaire (utile pour les tests d'intégration ou un déploiement
// où il n'est pas sur le PATH) ; une valeur vide utilise "pdftotext".
type PdftotextExtractor struct {
	BinPath string
}

func (e PdftotextExtractor) ExtractPerPage(ctx context.Context, path string) ([]PageText, error) {
	bin := e.BinPath
	if bin == "" {
		bin = "pdftotext"
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-layout", path, "-")
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("triage: pdftotext failed on %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("triage: run pdftotext on %s: %w", path, err)
	}

	// pdftotext sépare chaque page par un form-feed (\f), y compris après
	// la dernière page : on obtient donc toujours un élément vide en trop
	// après le split, qu'on retire.
	raw := strings.Split(string(out), "\f")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}

	pages := make([]PageText, len(raw))
	for i, text := range raw {
		pages[i] = PageText{Page: i + 1, Text: text}
	}
	return pages, nil
}
