package store

import (
	"encoding/json"
	"fmt"
	"os"
)

// ReadDocumentRecord et ReadPageRecord lisent un enregistrement persisté.
// Un enregistrement dont schema_version est inférieur à la version
// courante est refusé plutôt que migré à la volée : la migration est un
// acte de déploiement délibéré (`jarvis migrate --out-dir DIR`), jamais
// une transformation silencieuse déclenchée par une lecture.
func ReadDocumentRecord(path string) (DocumentRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DocumentRecord{}, fmt.Errorf("store: read %s: %w", path, err)
	}

	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return DocumentRecord{}, fmt.Errorf("store: %s is not valid JSON: %w", path, err)
	}
	if v := recordVersion(probe); v < CurrentDocumentRecordVersion {
		return DocumentRecord{}, fmt.Errorf("store: %s is at schema_version %d, current is %d — run `jarvis migrate` first", path, v, CurrentDocumentRecordVersion)
	}

	var rec DocumentRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return DocumentRecord{}, fmt.Errorf("store: decode %s: %w", path, err)
	}
	return rec, nil
}

func ReadPageRecord(path string) (PageRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PageRecord{}, fmt.Errorf("store: read %s: %w", path, err)
	}

	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return PageRecord{}, fmt.Errorf("store: %s is not valid JSON: %w", path, err)
	}
	if v := recordVersion(probe); v < CurrentPageRecordVersion {
		return PageRecord{}, fmt.Errorf("store: %s is at schema_version %d, current is %d — run `jarvis migrate` first", path, v, CurrentPageRecordVersion)
	}

	var rec PageRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return PageRecord{}, fmt.Errorf("store: decode %s: %w", path, err)
	}
	return rec, nil
}
