package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendRunLog_CreatesFileWithOneLine(t *testing.T) {
	dir := t.TempDir()
	entry := RunLogEntry{
		Timestamp:  fixedTime,
		SourceHash: "H",
		SourcePath: "doc.pdf",
		DocType:    "facture",
		PagesTotal: 2,
	}

	if err := AppendRunLog(dir, "H", entry); err != nil {
		t.Fatalf("AppendRunLog() error = %v, want nil", err)
	}

	lines := readLines(t, filepath.Join(dir, "H", "runs.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	var got RunLogEntry
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line is not valid JSON: %v", err)
	}
	if got.SourceHash != "H" || got.PagesTotal != 2 {
		t.Errorf("logged entry = %+v, want matching hash/pages", got)
	}
}

func TestAppendRunLog_MultipleCallsAppend(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := AppendRunLog(dir, "H", RunLogEntry{Timestamp: fixedTime, SourceHash: "H", PagesTotal: i}); err != nil {
			t.Fatal(err)
		}
	}

	lines := readLines(t, filepath.Join(dir, "H", "runs.jsonl"))
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (append-only across runs)", len(lines))
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
