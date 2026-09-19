package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRawFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadDocumentRecord_CurrentVersion_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.json")
	writeRawFile(t, path, `{"source_hash":"H","schema_version":1,"pages":[1]}`)

	got, err := ReadDocumentRecord(path)
	if err != nil {
		t.Fatalf("ReadDocumentRecord() error = %v, want nil", err)
	}
	if got.SourceHash != "H" || len(got.Pages) != 1 {
		t.Errorf("got = %+v, want SourceHash=H Pages=[1]", got)
	}
}

func TestReadDocumentRecord_OutdatedVersion_ReturnsActionableError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.json")
	writeRawFile(t, path, `{"source_hash":"H","schema_version":0,"pages":[1]}`)

	_, err := ReadDocumentRecord(path)
	if err == nil {
		t.Fatal("ReadDocumentRecord() error = nil, want non-nil for an outdated record")
	}
	if !strings.Contains(err.Error(), "jarvis migrate") {
		t.Errorf("error = %v, want it to mention `jarvis migrate`", err)
	}
}

func TestReadDocumentRecord_NonExistentFile_ReturnsError(t *testing.T) {
	_, err := ReadDocumentRecord("does-not-exist.json")
	if err == nil {
		t.Fatal("ReadDocumentRecord() error = nil, want non-nil for a missing file")
	}
}

func TestReadDocumentRecord_MalformedJSON_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.json")
	writeRawFile(t, path, `not json`)

	_, err := ReadDocumentRecord(path)
	if err == nil {
		t.Fatal("ReadDocumentRecord() error = nil, want non-nil for malformed JSON")
	}
}

func TestReadPageRecord_CurrentVersion_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page-1.json")
	writeRawFile(t, path, `{"source_hash":"H","schema_version":1,"page":1,"source":"native"}`)

	got, err := ReadPageRecord(path)
	if err != nil {
		t.Fatalf("ReadPageRecord() error = %v, want nil", err)
	}
	if got.Page != 1 || got.Source != SourceNative {
		t.Errorf("got = %+v, want Page=1 Source=native", got)
	}
}

func TestReadPageRecord_OutdatedVersion_ReturnsActionableError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page-1.json")
	writeRawFile(t, path, `{"source_hash":"H","page":1}`) // pas de schema_version -> version 0

	_, err := ReadPageRecord(path)
	if err == nil {
		t.Fatal("ReadPageRecord() error = nil, want non-nil for an outdated record")
	}
	if !strings.Contains(err.Error(), "jarvis migrate") {
		t.Errorf("error = %v, want it to mention `jarvis migrate`", err)
	}
}
