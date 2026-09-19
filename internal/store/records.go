package store

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// Source indique d'où vient le contenu d'une page persistée — reflète
// pipeline.Source, redéclaré ici pour ne pas coupler le format de
// stockage (une API publique de fait, une fois écrite sur disque) au
// détail interne du paquet pipeline.
type Source string

const (
	SourceNative Source = "native"
	SourceVLM    Source = "vlm"
)

// DocumentRecord résume le traitement d'un document entier. Écrit dans
// <out-dir>/<hash>/document.json.
type DocumentRecord struct {
	SourceHash   string    `json:"source_hash"`
	SourcePath   string    `json:"source_path"`
	DocType      string    `json:"doc_type"`
	ProcessedAt  time.Time `json:"processed_at"`
	TriageScore  float64   `json:"triage_score"`
	HasTextLayer bool      `json:"has_text_layer"`
	Pages        []int     `json:"pages"`
}

// PageRecord est l'enregistrement complet d'une page : sa provenance
// (texte natif ou VLM) et son résultat d'extraction, avec le modèle et le
// prompt utilisés à chaque étage. Écrit dans
// <out-dir>/<hash>/page-<N>.json.
type PageRecord struct {
	SourceHash  string    `json:"source_hash"`
	SourcePath  string    `json:"source_path"`
	DocType     string    `json:"doc_type"`
	Page        int       `json:"page"`
	ProcessedAt time.Time `json:"processed_at"`

	Source Source `json:"source,omitempty"`

	Parsing    *PageParsing    `json:"parsing,omitempty"`
	Extraction *PageExtraction `json:"extraction,omitempty"`
}

// PageParsing est la provenance de l'étage Parsing pour une page, si elle
// y est passée (page sans texte natif exploitable).
type PageParsing struct {
	Markdown     string `json:"markdown"`
	Model        string `json:"model"`
	ModelVersion string `json:"model_version"`
	Prompt       string `json:"prompt"`
	Failed       bool   `json:"failed"`
	Error        string `json:"error,omitempty"`
}

// PageExtraction est le résultat de l'étage Extraction pour une page.
type PageExtraction struct {
	JSON                json.RawMessage `json:"json,omitempty"`
	Model               string          `json:"model"`
	ModelVersion        string          `json:"model_version"`
	Prompt              string          `json:"prompt"`
	NeedsReview         bool            `json:"needs_review"`
	LowConfidenceFields []string        `json:"low_confidence_fields,omitempty"`
	Failed              bool            `json:"failed"`
	Error               string          `json:"error,omitempty"`
}

// BuildRecords assemble un DocumentRecord et ses PageRecord à partir d'un
// pipeline.Result. Fonction pure : aucune I/O, testable sans disque.
func BuildRecords(sourceHash, sourcePath, docType string, processedAt time.Time, result pipeline.Result) (DocumentRecord, []PageRecord) {
	extractionByPage := make(map[int]int, len(result.Extraction))
	for i, e := range result.Extraction {
		extractionByPage[e.Page] = i
	}
	parsingIdxByPage := make(map[int]int, len(result.Parsing))
	for i, p := range result.Parsing {
		parsingIdxByPage[p.Page] = i
	}

	pages := make([]PageRecord, 0, len(result.Triage.Pages))
	pageNumbers := make([]int, 0, len(result.Triage.Pages))

	for _, tp := range result.Triage.Pages {
		rec := PageRecord{
			SourceHash:  sourceHash,
			SourcePath:  sourcePath,
			DocType:     docType,
			Page:        tp.Page,
			ProcessedAt: processedAt,
		}

		if tp.Usable {
			rec.Source = SourceNative
		} else if idx, ok := parsingIdxByPage[tp.Page]; ok {
			rec.Source = SourceVLM
			p := result.Parsing[idx]
			rec.Parsing = &PageParsing{
				Markdown:     p.Markdown,
				Model:        p.Model.Name,
				ModelVersion: p.Model.Version,
				Prompt:       p.Prompt,
				Failed:       p.Failed,
				Error:        p.Error,
			}
		}

		if idx, ok := extractionByPage[tp.Page]; ok {
			e := result.Extraction[idx]
			rec.Extraction = &PageExtraction{
				JSON:                e.JSON,
				Model:               e.Model.Name,
				ModelVersion:        e.Model.Version,
				Prompt:              e.Prompt,
				NeedsReview:         e.NeedsReview,
				LowConfidenceFields: e.LowConfidenceFields,
				Failed:              e.Failed,
				Error:               e.Error,
			}
		}

		pages = append(pages, rec)
		pageNumbers = append(pageNumbers, tp.Page)
	}

	sort.Slice(pages, func(i, j int) bool { return pages[i].Page < pages[j].Page })
	sort.Ints(pageNumbers)

	doc := DocumentRecord{
		SourceHash:   sourceHash,
		SourcePath:   sourcePath,
		DocType:      docType,
		ProcessedAt:  processedAt,
		TriageScore:  result.Triage.Score,
		HasTextLayer: result.Triage.HasTextLayer,
		Pages:        pageNumbers,
	}

	return doc, pages
}
