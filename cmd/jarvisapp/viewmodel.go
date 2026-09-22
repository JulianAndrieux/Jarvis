package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// buildResultView transforme le résultat brut du pipeline en une vue
// prête pour l'affichage, sans présumer des champs d'un type de document
// particulier (le JSON de chaque page est aplati génériquement).
func buildResultView(job webapp.Job) templates.ResultView {
	if job.Result == nil {
		return templates.ResultView{}
	}
	result := job.Result

	pages := make([]templates.PageView, 0, len(result.Extraction))
	for _, e := range result.Extraction {
		pv := templates.PageView{
			Page:        e.Page,
			NeedsReview: e.NeedsReview,
			Failed:      e.Failed,
			Error:       e.Error,
			Model:       modelLabel(e.Model.Name, e.Model.Version),
			Prompt:      e.Prompt,
		}
		if !e.Failed && len(e.JSON) > 0 {
			pv.Fields = flattenExtractionJSON(e.JSON)
			pv.RawJSON = prettyJSON(e.JSON)
		}
		pages = append(pages, pv)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Page < pages[j].Page })

	merged := templates.PageView{
		NeedsReview: result.Merged.NeedsReview,
		Failed:      result.Merged.Failed,
		Error:       result.Merged.Error,
	}
	if !result.Merged.Failed && len(result.Merged.JSON) > 0 {
		merged.Fields = flattenExtractionJSON(result.Merged.JSON)
	}

	ocrPages, nativeTextMissing := buildOCRPages(result)

	return templates.ResultView{
		DocType:                  job.DocType,
		PageCount:                len(result.Triage.Pages),
		OCRPages:                 ocrPages,
		NativeTextMissing:        nativeTextMissing,
		TriageScore:              result.Triage.Score,
		HasTextLayer:             result.Triage.HasTextLayer,
		Pages:                    pages,
		Merged:                   merged,
		ClassificationConfidence: result.ClassificationConfidence,
	}
}

// flattenExtractionJSON décode raw et aplatit chaque champ schema.Field
// (détecté via extraction.IsFieldNode) en une FieldView, triée par nom
// pour un affichage stable.
func flattenExtractionJSON(raw json.RawMessage) []templates.FieldView {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}

	var fields []templates.FieldView
	flattenNode("", decoded, &fields)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields
}

func flattenNode(prefix string, node any, out *[]templates.FieldView) {
	switch v := node.(type) {
	case map[string]any:
		if extraction.IsFieldNode(v) {
			*out = append(*out, fieldViewFrom(prefix, v))
			return
		}
		for key, child := range v {
			flattenNode(joinFieldPath(prefix, key), child, out)
		}
	case []any:
		for i, child := range v {
			flattenNode(fmt.Sprintf("%s[%d]", prefix, i), child, out)
		}
	}
}

func fieldViewFrom(name string, m map[string]any) templates.FieldView {
	fv := templates.FieldView{Name: name}
	if val, ok := m["value"]; ok {
		fv.Value = fmt.Sprint(val)
	}
	if c, ok := m["confidence"].(float64); ok {
		fv.Confidence = c
	}
	if s, ok := m["source_snippet"].(string); ok {
		fv.SourceSnippet = s
	}
	if b, ok := m["bbox"].(map[string]any); ok {
		fv.BBox = fmt.Sprintf("x:%.0f–%.0f y:%.0f–%.0f", numOrZero(b["x_min"]), numOrZero(b["x_max"]), numOrZero(b["y_min"]), numOrZero(b["y_max"]))
	}
	return fv
}

func numOrZero(v any) float64 {
	f, _ := v.(float64)
	return f
}

func joinFieldPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// buildOCRPages construit l'onglet "Texte OCR" (jalon 22) : le texte de
// chaque page tel qu'envoyé au LLM, avec sa provenance. Source de vérité :
// Result.Pages. Un résultat antérieur au jalon 22 n'a que le Markdown VLM
// (Result.Parsing) — il est affiché, et nativeTextMissing signale que le
// texte des pages natives n'a pas été conservé (une ré-extraction le
// capture).
func buildOCRPages(result *pipeline.Result) (pages []templates.OCRPageView, nativeTextMissing bool) {
	vlmModels := make(map[int]string, len(result.Parsing))
	for _, p := range result.Parsing {
		vlmModels[p.Page] = modelLabel(p.Model.Name, p.Model.Version)
		if p.Failed {
			pages = append(pages, templates.OCRPageView{Page: p.Page, Source: "VLM", Model: vlmModels[p.Page], Failed: true, Error: p.Error})
		}
	}

	if len(result.Pages) > 0 {
		for _, p := range result.Pages {
			pv := templates.OCRPageView{Page: p.Page, Text: p.Text, Source: "Texte natif"}
			if p.Source == pipeline.SourceVLM {
				pv.Source, pv.Model = "VLM", vlmModels[p.Page]
			}
			pages = append(pages, pv)
		}
	} else {
		for _, p := range result.Parsing {
			if !p.Failed {
				pages = append(pages, templates.OCRPageView{Page: p.Page, Text: p.Markdown, Source: "VLM", Model: vlmModels[p.Page]})
			}
		}
		for _, tp := range result.Triage.Pages {
			if tp.Usable {
				nativeTextMissing = true
				break
			}
		}
	}

	sort.Slice(pages, func(i, j int) bool { return pages[i].Page < pages[j].Page })
	return pages, nativeTextMissing
}

func modelLabel(name, version string) string {
	if version == "" {
		return name
	}
	return name + " " + version
}

// prettyJSON indente raw pour l'affichage ; raw tel quel s'il n'est pas
// du JSON valide (on montre ce que le LLM a produit, jamais rien).
func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
