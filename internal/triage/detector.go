package triage

import (
	"context"
	"fmt"
)

// Detector combine un TextExtractor et des Thresholds pour trancher, pour
// un PDF donné, s'il a une couche texte exploitable.
type Detector struct {
	Extractor  TextExtractor
	Thresholds Thresholds
}

// zeroThresholds est la valeur zéro de Thresholds — utilisée pour détecter
// qu'aucun seuil n'a été explicitement fourni et retomber sur
// DefaultThresholds().
var zeroThresholds Thresholds

// Detect extrait le texte de path via d.Extractor puis calcule le score de
// triage. Si d.Thresholds est la valeur zéro (non renseignée), les seuils
// par défaut sont utilisés.
func (d Detector) Detect(ctx context.Context, path string) (Result, error) {
	th := d.Thresholds
	if th == zeroThresholds {
		th = DefaultThresholds()
	}

	pages, err := d.Extractor.ExtractPerPage(ctx, path)
	if err != nil {
		return Result{}, fmt.Errorf("triage: detect %s: %w", path, err)
	}

	return Score(pages, th), nil
}
