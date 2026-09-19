package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMigrateFixture(t *testing.T, dir, hash string, files map[string]string) string {
	t.Helper()
	hashDir := filepath.Join(dir, hash)
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(hashDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hashDir
}

func TestRunMigrate_RequiresOutDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"migrate"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want non-nil when --out-dir is missing")
	}
}

func TestRunMigrate_NonExistentOutDir_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"migrate", "--out-dir", "/does/not/exist"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want non-nil for a missing out-dir")
	}
}

func TestRunMigrate_MigratesOutdatedRecords(t *testing.T) {
	dir := t.TempDir()
	hashDir := writeMigrateFixture(t, dir, "HASH1", map[string]string{
		"document.json": `{"source_hash":"HASH1","triage_score":1,"has_text_layer":true,"pages":[1]}`,
		"page-1.json":   `{"source_hash":"HASH1","page":1,"source":"native"}`,
		"runs.jsonl":    `{"source_hash":"HASH1"}` + "\n",
	})

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"migrate", "--out-dir", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil (stdout=%s)", err, stdout.String())
	}

	if !strings.Contains(stdout.String(), "document.json") || !strings.Contains(stdout.String(), "page-1.json") {
		t.Errorf("stdout does not report both migrated files: %s", stdout.String())
	}

	docBytes, err := os.ReadFile(filepath.Join(hashDir, "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(docBytes, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["schema_version"] != float64(1) {
		t.Errorf("document.json schema_version = %v, want 1", doc["schema_version"])
	}
	if doc["triage_score"] != float64(1) || doc["pages"] == nil {
		t.Errorf("document.json lost business data after migration: %+v", doc)
	}

	pageBytes, err := os.ReadFile(filepath.Join(hashDir, "page-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(pageBytes, &page); err != nil {
		t.Fatal(err)
	}
	if page["schema_version"] != float64(1) || page["source"] != "native" {
		t.Errorf("page-1.json = %+v, want schema_version=1 source=native", page)
	}

	// runs.jsonl n'est pas un enregistrement à migrer : doit rester
	// inchangé (pas de champ schema_version ajouté).
	runsBytes, err := os.ReadFile(filepath.Join(hashDir, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runsBytes), "schema_version") {
		t.Errorf("runs.jsonl was touched by migrate, want it untouched: %s", runsBytes)
	}
}

func TestRunMigrate_DryRun_DoesNotWriteFiles(t *testing.T) {
	dir := t.TempDir()
	hashDir := writeMigrateFixture(t, dir, "HASH1", map[string]string{
		"document.json": `{"source_hash":"HASH1","pages":[1]}`,
	})
	original, err := os.ReadFile(filepath.Join(hashDir, "document.json"))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"migrate", "--out-dir", dir, "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "document.json") {
		t.Errorf("dry-run stdout does not mention the file that would be migrated: %s", stdout.String())
	}

	after, err := os.ReadFile(filepath.Join(hashDir, "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Errorf("--dry-run modified the file on disk: before=%s after=%s", original, after)
	}
}

func TestRunMigrate_AlreadyCurrent_ReportsNothingToDo(t *testing.T) {
	dir := t.TempDir()
	writeMigrateFixture(t, dir, "HASH1", map[string]string{
		"document.json": `{"source_hash":"HASH1","schema_version":1,"pages":[1]}`,
	})

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"migrate", "--out-dir", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if strings.Contains(stdout.String(), "document.json") {
		t.Errorf("stdout mentions an already-current file as migrated: %s", stdout.String())
	}
}
