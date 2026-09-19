//go:build integration

// Dépend des binaires externes `pdftotext`/`pdftoppm` (poppler-utils),
// utilisés par la vraie implémentation des ports triage/parsing que
// `jarvis process` cablê en dur. Le VLM et le LLM, eux, sont des serveurs
// httptest en mémoire : ce test valide le câblage --out-dir de bout en
// bout sans dépendre d'un vrai modèle.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRun_Process_OutDir_PersistsResults(t *testing.T) {
	// native.pdf a une couche texte native : le VLM ne doit jamais être
	// appelé. Le stub VLM fait échouer le test s'il reçoit une requête.
	vlmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("VLM stub called, want it never called for a native-text page")
	}))
	defer vlmSrv.Close()

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"numero\":{\"value\":\"2026-0042\",\"confidence\":0.95,\"source_snippet\":\"Facture n. 2026-0042\"},\"fournisseur\":{\"value\":\"Acme SARL\",\"confidence\":0.9,\"source_snippet\":\"Acme SARL\"},\"total_ttc\":{\"value\":123.45,\"confidence\":0.9,\"source_snippet\":\"123.45\"}}"}}]}`))
	}))
	defer llmSrv.Close()

	outDir := t.TempDir()
	pdfPath := filepath.Join("..", "..", "testdata", "fixtures", "native.pdf")

	args := []string{
		"process",
		"--vlm-url", vlmSrv.URL,
		"--vlm-model", "vlm-test",
		"--llm-url", llmSrv.URL,
		"--llm-model", "llm-test",
		"--doc-type", "facture",
		"--out-dir", outDir,
		pdfPath,
	}

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil (stdout=%q)", err, stdout.String())
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read out-dir: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("out-dir entries = %v, want exactly one hash directory", entries)
	}
	hashDir := filepath.Join(outDir, entries[0].Name())

	docBytes, err := os.ReadFile(filepath.Join(hashDir, "document.json"))
	if err != nil {
		t.Fatalf("read document.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(docBytes, &doc); err != nil {
		t.Fatalf("document.json is not valid JSON: %v", err)
	}
	if doc["has_text_layer"] != true {
		t.Errorf("document.json has_text_layer = %v, want true", doc["has_text_layer"])
	}

	pageBytes, err := os.ReadFile(filepath.Join(hashDir, "page-1.json"))
	if err != nil {
		t.Fatalf("read page-1.json: %v", err)
	}
	var page map[string]any
	if err := json.Unmarshal(pageBytes, &page); err != nil {
		t.Fatalf("page-1.json is not valid JSON: %v", err)
	}
	if page["source"] != "native" {
		t.Errorf("page-1.json source = %v, want native", page["source"])
	}
	extraction, ok := page["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("page-1.json has no extraction object: %+v", page)
	}
	if extraction["model"] != "llm-test" {
		t.Errorf("extraction.model = %v, want llm-test", extraction["model"])
	}

	// La page est en texte natif et le snippet est recopié tel quel du
	// texte source : le bbox doit avoir été localisé et attaché par le
	// vrai PdftotextBBoxExtractor (pas un fake).
	extractedJSON, ok := extraction["json"].(map[string]any)
	if !ok {
		t.Fatalf("extraction.json is not an object: %+v", extraction)
	}
	numero, ok := extractedJSON["numero"].(map[string]any)
	if !ok {
		t.Fatalf("extraction.json.numero is not an object: %+v", extractedJSON)
	}
	b, ok := numero["bbox"].(map[string]any)
	if !ok {
		t.Fatalf("numero.bbox missing, want it attached from the real bbox extractor: %+v", numero)
	}
	if b["x_min"].(float64) <= 0 || b["x_max"].(float64) <= b["x_min"].(float64) {
		t.Errorf("numero.bbox looks invalid: %+v", b)
	}

	runLogBytes, err := os.ReadFile(filepath.Join(hashDir, "runs.jsonl"))
	if err != nil {
		t.Fatalf("read runs.jsonl: %v", err)
	}
	if len(bytes.TrimSpace(runLogBytes)) == 0 {
		t.Error("runs.jsonl is empty, want at least one logged run")
	}
}
