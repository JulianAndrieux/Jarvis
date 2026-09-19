package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

// triageOutput est le format JSON stable exposé par `jarvis triage`.
// Distinct de triage.Result pour ne pas coupler la sortie CLI aux détails
// internes du package (ex. renommer un champ interne ne doit pas casser le
// contrat CLI).
type triageOutput struct {
	HasTextLayer bool               `json:"has_text_layer"`
	Score        float64            `json:"score"`
	Reasons      []string           `json:"reasons"`
	Pages        []triagePageOutput `json:"pages"`
}

type triagePageOutput struct {
	Page           int     `json:"page"`
	CharCount      int     `json:"char_count"`
	PrintableRatio float64 `json:"printable_ratio"`
	LetterRatio    float64 `json:"letter_ratio"`
	Usable         bool    `json:"usable"`
}

func runTriage(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: jarvis triage <fichier.pdf>")
	}
	path := args[0]

	d := triage.Detector{
		Extractor:  triage.PdftotextExtractor{},
		Thresholds: triage.DefaultThresholds(),
	}

	result, err := d.Detect(ctx, path)
	if err != nil {
		return fmt.Errorf("triage %s: %w", path, err)
	}

	return json.NewEncoder(stdout).Encode(toTriageOutput(result))
}

func toTriageOutput(r triage.Result) triageOutput {
	pages := make([]triagePageOutput, len(r.Pages))
	for i, p := range r.Pages {
		pages[i] = triagePageOutput{
			Page:           p.Page,
			CharCount:      p.CharCount,
			PrintableRatio: p.PrintableRatio,
			LetterRatio:    p.LetterRatio,
			Usable:         p.Usable,
		}
	}
	return triageOutput{
		HasTextLayer: r.HasTextLayer,
		Score:        r.Score,
		Reasons:      r.Reasons,
		Pages:        pages,
	}
}
