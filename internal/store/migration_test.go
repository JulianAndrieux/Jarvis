package store

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestRecordVersion_MissingKey_ReturnsZero(t *testing.T) {
	got := recordVersion(map[string]any{"source_hash": "H"})
	if got != 0 {
		t.Errorf("recordVersion() = %d, want 0 for a record with no schema_version", got)
	}
}

func TestRecordVersion_Present(t *testing.T) {
	got := recordVersion(map[string]any{"schema_version": float64(3)})
	if got != 3 {
		t.Errorf("recordVersion() = %d, want 3", got)
	}
}

func TestRecordVersion_FromRawJSON(t *testing.T) {
	got, err := RecordVersion(json.RawMessage(`{"schema_version":2}`))
	if err != nil {
		t.Fatalf("RecordVersion() error = %v, want nil", err)
	}
	if got != 2 {
		t.Errorf("RecordVersion() = %d, want 2", got)
	}
}

func TestRecordVersion_MissingField_ReturnsZero(t *testing.T) {
	got, err := RecordVersion(json.RawMessage(`{"source_hash":"H"}`))
	if err != nil {
		t.Fatalf("RecordVersion() error = %v, want nil", err)
	}
	if got != 0 {
		t.Errorf("RecordVersion() = %d, want 0", got)
	}
}

func TestRecordVersion_MalformedJSON_ReturnsError(t *testing.T) {
	_, err := RecordVersion(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("RecordVersion() error = nil, want non-nil for malformed JSON")
	}
}

func TestMigrateDocumentJSON_MissingVersion_UpgradesToCurrent(t *testing.T) {
	raw := json.RawMessage(`{"source_hash":"H","source_path":"doc.pdf","doc_type":"facture","triage_score":1,"has_text_layer":true,"pages":[1]}`)

	got, err := MigrateDocumentJSON(raw)
	if err != nil {
		t.Fatalf("MigrateDocumentJSON() error = %v, want nil", err)
	}

	var rec DocumentRecord
	if err := json.Unmarshal(got, &rec); err != nil {
		t.Fatalf("migrated JSON does not decode: %v", err)
	}
	if rec.SchemaVersion != CurrentDocumentRecordVersion {
		t.Errorf("SchemaVersion = %d, want %d", rec.SchemaVersion, CurrentDocumentRecordVersion)
	}
	// Le contenu métier doit être préservé.
	if rec.SourceHash != "H" || rec.TriageScore != 1 || !rec.HasTextLayer || len(rec.Pages) != 1 {
		t.Errorf("migrated record lost data: %+v", rec)
	}
}

func TestMigrateDocumentJSON_V1ToV2_PreservesContent(t *testing.T) {
	// v1 n'avait pas MergedExtraction : vérifie que la migration 1->2
	// (purement additive) ne perd aucune donnée existante.
	raw := json.RawMessage(`{"source_hash":"H","schema_version":1,"pages":[1,2],"triage_score":1,"has_text_layer":true}`)

	got, err := MigrateDocumentJSON(raw)
	if err != nil {
		t.Fatalf("MigrateDocumentJSON() error = %v, want nil", err)
	}

	var rec DocumentRecord
	if err := json.Unmarshal(got, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.SchemaVersion != CurrentDocumentRecordVersion || rec.SourceHash != "H" || len(rec.Pages) != 2 {
		t.Errorf("record = %+v, want SchemaVersion=%d SourceHash=H Pages=[1 2]", rec, CurrentDocumentRecordVersion)
	}
	if rec.MergedExtraction != nil {
		t.Errorf("MergedExtraction = %+v, want nil (absent from the v1 source)", rec.MergedExtraction)
	}
}

func TestMigrateDocumentJSON_AlreadyCurrent_PreservesContent(t *testing.T) {
	raw := json.RawMessage(fmt.Sprintf(`{"source_hash":"H","schema_version":%d,"pages":[1,2]}`, CurrentDocumentRecordVersion))

	got, err := MigrateDocumentJSON(raw)
	if err != nil {
		t.Fatalf("MigrateDocumentJSON() error = %v, want nil", err)
	}

	var rec DocumentRecord
	if err := json.Unmarshal(got, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.SchemaVersion != CurrentDocumentRecordVersion || rec.SourceHash != "H" || len(rec.Pages) != 2 {
		t.Errorf("record = %+v, want unchanged content at the current version", rec)
	}
}

func TestMigrateDocumentJSON_MalformedJSON_ReturnsError(t *testing.T) {
	_, err := MigrateDocumentJSON(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("MigrateDocumentJSON() error = nil, want non-nil for malformed JSON")
	}
}

func TestMigratePageJSON_MissingVersion_UpgradesToCurrent(t *testing.T) {
	raw := json.RawMessage(`{"source_hash":"H","page":1,"source":"native"}`)

	got, err := MigratePageJSON(raw)
	if err != nil {
		t.Fatalf("MigratePageJSON() error = %v, want nil", err)
	}

	var rec PageRecord
	if err := json.Unmarshal(got, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.SchemaVersion != CurrentPageRecordVersion || rec.Page != 1 || rec.Source != SourceNative {
		t.Errorf("migrated record = %+v, want SchemaVersion=%d Page=1 Source=native", rec, CurrentPageRecordVersion)
	}
}

func TestMigratePageJSON_MalformedJSON_ReturnsError(t *testing.T) {
	_, err := MigratePageJSON(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("MigratePageJSON() error = nil, want non-nil for malformed JSON")
	}
}

// TestMigrations_HaveNonEmptyDescriptions vérifie qu'aucune Migration
// enregistrée n'a une Description vide — la description est le "quoi
// faire" exigé pour tout changement de struct persistée.
func TestMigrations_HaveNonEmptyDescriptions(t *testing.T) {
	for version, m := range documentMigrations {
		if m.Description == "" {
			t.Errorf("documentMigrations[%d] has an empty Description", version)
		}
	}
	for version, m := range pageMigrations {
		if m.Description == "" {
			t.Errorf("pageMigrations[%d] has an empty Description", version)
		}
	}
}
