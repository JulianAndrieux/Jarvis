// Package pipeline enchaîne les trois étages (Triage, Parsing, Extraction)
// pour un document entier. C'est la seule couche qui les connaît tous les
// trois : chaque étage reste par ailleurs testable et utilisable seul.
package pipeline

import (
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

// Source indique d'où vient le texte d'une page.
type Source string

const (
	SourceNative Source = "native"
	SourceVLM    Source = "vlm"
)

// PageContent est le texte d'une page, quelle que soit sa provenance
// (texte natif via internal/triage, ou Markdown produit par
// internal/vlm), prêt pour l'étage Extraction.
type PageContent struct {
	Page   int
	Text   string
	Source Source
}

// Merge combine les pages jugées "usable" par le triage (texte natif) et
// les pages parsées avec succès par le VLM (Markdown) en une liste
// unique, triée par numéro de page (l'ordre de native).
//
// Une page non-usable dont le parsing VLM a échoué (parsing.PageResult.
// Failed) est exclue : il n'existe alors aucun contenu exploitable pour
// cette page, cohérent avec la stratégie d'échec retenue (marquage
// immédiat, pas de fallback silencieux sur un texte vide ou inventé).
func Merge(native []triage.PageText, triageResult triage.Result, parsed []parsing.PageResult) []PageContent {
	usable := make(map[int]bool, len(triageResult.Pages))
	for _, pr := range triageResult.Pages {
		usable[pr.Page] = pr.Usable
	}

	parsedByPage := make(map[int]parsing.PageResult, len(parsed))
	for _, p := range parsed {
		parsedByPage[p.Page] = p
	}

	var out []PageContent
	for _, np := range native {
		if usable[np.Page] {
			out = append(out, PageContent{Page: np.Page, Text: np.Text, Source: SourceNative})
			continue
		}
		if pr, ok := parsedByPage[np.Page]; ok && !pr.Failed {
			out = append(out, PageContent{Page: pr.Page, Text: pr.Markdown, Source: SourceVLM})
		}
	}
	return out
}
