package pipeline

import (
	"sort"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
)

// Stage est l'étape en cours d'un traitement, pour le suivi de
// progression (jalon 23).
type Stage string

const (
	StageParsing     Stage = "parsing"
	StageClassifying Stage = "classification"
	StageExtracting  Stage = "extraction"
)

// Progress est l'état d'avancement d'un traitement, transmis après le
// triage puis après chaque page lue (VLM) ou extraite (LLM) — jalon 23 :
// une page scannée dense prend ~3-4 min au VLM sur la machine cible, le
// texte déjà lu doit pouvoir être enregistré et affiché sans attendre la
// fin du document.
type Progress struct {
	Stage Stage
	// PageCount est le nombre de pages du document.
	PageCount int
	// ParseTotal/ParseDone : pages à passer / déjà passées par le VLM
	// (échecs compris dans ParseDone).
	ParseTotal, ParseDone int
	// ExtractTotal/ExtractDone : pages à extraire / déjà extraites par le
	// LLM. Zéro avant l'étape d'extraction.
	ExtractTotal, ExtractDone int
	// Pages est le texte déjà disponible (natif dès le triage, VLM au fil
	// de l'eau), trié par page — la même forme que Result.Pages.
	Pages []PageContent
	// ParseFailures sont les pages VLM en échec jusqu'ici.
	ParseFailures []parsing.PageResult
}

// ProgressFunc reçoit chaque étape de progression. Chaque Progress reçu
// est un instantané indépendant : il peut être conservé tel quel.
// Les appels sont séquentiels (jamais concurrents), une ProgressFunc n'a
// pas à être sûre en concurrence.
type ProgressFunc func(Progress)

// progressTracker accumule l'état courant et en émet des instantanés.
// Un tracker sans fonction (fn == nil) ne fait rien : les chemins sans
// suivi (CLI) n'ont rien à vérifier.
type progressTracker struct {
	fn ProgressFunc
	p  Progress
}

func (t *progressTracker) emit() {
	if t.fn == nil {
		return
	}
	snapshot := t.p
	snapshot.Pages = append([]PageContent(nil), t.p.Pages...)
	snapshot.ParseFailures = append([]parsing.PageResult(nil), t.p.ParseFailures...)
	t.fn(snapshot)
}

func (t *progressTracker) triaged(pageCount, parseTotal int, native []PageContent) {
	t.p.Stage = StageParsing
	t.p.PageCount = pageCount
	t.p.ParseTotal = parseTotal
	t.p.Pages = append(t.p.Pages, native...)
	t.emit()
}

func (t *progressTracker) pageParsed(r parsing.PageResult) {
	t.p.ParseDone++
	if r.Failed {
		t.p.ParseFailures = append(t.p.ParseFailures, r)
	} else {
		t.p.Pages = append(t.p.Pages, PageContent{Page: r.Page, Text: r.Markdown, Source: SourceVLM})
		sort.Slice(t.p.Pages, func(i, j int) bool { return t.p.Pages[i].Page < t.p.Pages[j].Page })
	}
	t.emit()
}

func (t *progressTracker) stage(s Stage) {
	t.p.Stage = s
	t.emit()
}

func (t *progressTracker) extracting(total int) {
	t.p.Stage = StageExtracting
	t.p.ExtractTotal = total
	t.emit()
}

func (t *progressTracker) pageExtracted(extraction.Result) {
	t.p.ExtractDone++
	t.emit()
}
