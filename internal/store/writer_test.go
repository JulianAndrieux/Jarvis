package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRecords_CreatesDocumentAndPageFiles(t *testing.T) {
	dir := t.TempDir()
	doc := DocumentRecord{
		RecordMeta: RecordMeta{SourceHash: "ABC123", SourcePath: "doc.pdf", DocType: "facture", ProcessedAt: fixedTime},
		Pages:      []int{1, 2},
	}
	pages := []PageRecord{
		{RecordMeta: RecordMeta{SourceHash: "ABC123"}, Page: 1, Source: SourceNative},
		{RecordMeta: RecordMeta{SourceHash: "ABC123"}, Page: 2, Source: SourceVLM},
	}

	if err := WriteRecords(dir, doc, pages); err != nil {
		t.Fatalf("WriteRecords() error = %v, want nil", err)
	}

	docPath := filepath.Join(dir, "ABC123", "document.json")
	docBytes, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	var gotDoc DocumentRecord
	if err := json.Unmarshal(docBytes, &gotDoc); err != nil {
		t.Fatalf("%s is not valid JSON: %v", docPath, err)
	}
	if gotDoc.SourceHash != "ABC123" || gotDoc.DocType != "facture" {
		t.Errorf("document.json content = %+v, want matching hash/doctype", gotDoc)
	}

	for _, page := range []int{1, 2} {
		pagePath := filepath.Join(dir, "ABC123", pageFileName(page))
		pageBytes, err := os.ReadFile(pagePath)
		if err != nil {
			t.Fatalf("read %s: %v", pagePath, err)
		}
		var gotPage PageRecord
		if err := json.Unmarshal(pageBytes, &gotPage); err != nil {
			t.Fatalf("%s is not valid JSON: %v", pagePath, err)
		}
		if gotPage.Page != page {
			t.Errorf("%s: Page = %d, want %d", pagePath, gotPage.Page, page)
		}
	}
}

func TestWriteRecords_RerunOverwritesPreviousResult(t *testing.T) {
	dir := t.TempDir()
	doc := DocumentRecord{RecordMeta: RecordMeta{SourceHash: "H"}, Pages: []int{1}}

	if err := WriteRecords(dir, doc, []PageRecord{{RecordMeta: RecordMeta{SourceHash: "H"}, Page: 1, Source: SourceNative}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteRecords(dir, doc, []PageRecord{{RecordMeta: RecordMeta{SourceHash: "H"}, Page: 1, Source: SourceVLM}}); err != nil {
		t.Fatal(err)
	}

	pagePath := filepath.Join(dir, "H", pageFileName(1))
	var got PageRecord
	pageBytes, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pageBytes, &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceVLM {
		t.Errorf("Source = %q after rerun, want vlm (latest run wins)", got.Source)
	}
}

func TestWriteRecords_OutputIsIndentedJSON(t *testing.T) {
	dir := t.TempDir()
	doc := DocumentRecord{RecordMeta: RecordMeta{SourceHash: "H"}, Pages: []int{1}}
	if err := WriteRecords(dir, doc, []PageRecord{{RecordMeta: RecordMeta{SourceHash: "H"}, Page: 1}}); err != nil {
		t.Fatal(err)
	}

	docBytes, err := os.ReadFile(filepath.Join(dir, "H", "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsNewlineIndent(docBytes) {
		t.Error("document.json does not look indented, want human-readable JSON for replay/debugging")
	}
}

func containsNewlineIndent(b []byte) bool {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\n' && (b[i+1] == ' ' || b[i+1] == '\t') {
			return true
		}
	}
	return false
}
