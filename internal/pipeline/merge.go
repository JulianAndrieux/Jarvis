// Package pipeline enchaîne les trois étages (Triage, Parsing, Extraction)
// pour un document entier. C'est la seule couche qui les connaît tous les
// trois : chaque étage reste par ailleurs testable et utilisable seul.
package pipeline

import (
	"fmt"
	"sort"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
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

// withVLMFailures complète extractionResults avec une entrée Failed pour
// chaque page exclue par Merge faute de parsing VLM exploitable — sans
// cela, une telle page disparaissait purement et simplement de
// Result.Extraction (donc de la sortie CLI et de l'UI web), sans aucun
// signal visible, malgré l'erreur réelle déjà disponible dans
// parsing.PageResult.Error. Trouvé en retestant sur un vrai document
// scanné (cf. CLAUDE.md) : une page sur cinq manquait silencieusement du
// résultat final alors que le document se classifiait et s'affichait
// comme un succès. Cohérent avec la façon dont un échec d'extraction
// LLM est déjà signalé (extraction.Result.Failed) — ici la cause est en
// amont (VLM), pas le LLM lui-même, d'où un message dédié.
func withVLMFailures(extractionResults []extraction.Result, parseResults []parsing.PageResult) []extraction.Result {
	present := make(map[int]bool, len(extractionResults))
	for _, r := range extractionResults {
		present[r.Page] = true
	}

	out := extractionResults
	for _, pr := range parseResults {
		if !pr.Failed || present[pr.Page] {
			continue
		}
		out = append(out, extraction.Result{
			Page:   pr.Page,
			Failed: true,
			Error:  fmt.Sprintf("page non extraite : échec du parsing VLM : %s", pr.Error),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Page < out[j].Page })
	return out
}
