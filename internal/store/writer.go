package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteRecords écrit doc et pages sous dir/<doc.SourceHash>/ : un
// document.json et un page-<N>.json par page. Un rejeu du même document
// écrase les fichiers existants (le dernier run fait foi pour l'état
// courant ; l'historique des runs est dans runs.jsonl, cf. AppendRunLog).
func WriteRecords(dir string, doc DocumentRecord, pages []PageRecord) error {
	docDir := filepath.Join(dir, doc.SourceHash)
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		return fmt.Errorf("store: create %s: %w", docDir, err)
	}

	if err := writeIndentedJSON(filepath.Join(docDir, "document.json"), doc); err != nil {
		return err
	}

	for _, page := range pages {
		if err := writeIndentedJSON(filepath.Join(docDir, pageFileName(page.Page)), page); err != nil {
			return err
		}
	}
	return nil
}

func pageFileName(page int) string {
	return fmt.Sprintf("page-%d.json", page)
}

func writeIndentedJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("store: write %s: %w", path, err)
	}
	return nil
}
