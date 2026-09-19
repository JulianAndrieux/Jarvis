package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// structFingerprint capture les noms et types des champs exportés d'un
// struct, en aplatissant les champs embeddés (anonymes) — comme le fait
// encoding/json pour la sérialisation. Deux structs avec le même
// fingerprint ont la même forme JSON effective.
func structFingerprint(t reflect.Type) string {
	var sb strings.Builder
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			fmt.Fprintf(&sb, "%s:%s;", f.Name, f.Type.String())
		}
	}
	walk(t)
	return sb.String()
}

// documentRecordFingerprints/pageRecordFingerprints associent une version
// de schéma à la forme attendue de DocumentRecord/PageRecord à cette
// version. NORME : toucher à la forme de ces structs (champ ajouté,
// supprimé, renommé ou retypé — y compris via RecordMeta, qui est
// aplati) sans mettre à jour ces deux points fait échouer
// TestDocumentRecordSchema_ChangeRequiresMigration /
// TestPageRecordSchema_ChangeRequiresMigration.
//
// Pour faire évoluer la forme d'un enregistrement persisté :
//  1. Modifier le struct (records.go).
//  2. Incrémenter CurrentDocumentRecordVersion / CurrentPageRecordVersion
//     (meta.go).
//  3. Ajouter le nouveau fingerprint ici (lancer le test, copier la
//     valeur "got" qu'il rapporte).
//  4. Enregistrer une Migration dans documentMigrations/pageMigrations
//     (migration.go) avec une Description qui explique la conversion.
var documentRecordFingerprints = map[int]string{
	1: "SourceHash:string;SourcePath:string;DocType:string;ProcessedAt:time.Time;SchemaVersion:int;TriageScore:float64;HasTextLayer:bool;Pages:[]int;",
	2: "SourceHash:string;SourcePath:string;DocType:string;ProcessedAt:time.Time;SchemaVersion:int;TriageScore:float64;HasTextLayer:bool;Pages:[]int;MergedExtraction:*store.PageExtraction;",
}

var pageRecordFingerprints = map[int]string{
	1: "SourceHash:string;SourcePath:string;DocType:string;ProcessedAt:time.Time;SchemaVersion:int;Page:int;Source:store.Source;Parsing:*store.PageParsing;Extraction:*store.PageExtraction;",
}

func TestDocumentRecordSchema_ChangeRequiresMigration(t *testing.T) {
	checkSchemaNorm(t, schemaNormConfig{
		StructName:       "DocumentRecord",
		Type:             reflect.TypeOf(DocumentRecord{}),
		CurrentVersion:   CurrentDocumentRecordVersion,
		VersionConstName: "CurrentDocumentRecordVersion",
		Fingerprints:     documentRecordFingerprints,
		FingerprintsName: "documentRecordFingerprints",
		Migrations:       documentMigrations,
		MigrationsName:   "documentMigrations",
	})
}

func TestPageRecordSchema_ChangeRequiresMigration(t *testing.T) {
	checkSchemaNorm(t, schemaNormConfig{
		StructName:       "PageRecord",
		Type:             reflect.TypeOf(PageRecord{}),
		CurrentVersion:   CurrentPageRecordVersion,
		VersionConstName: "CurrentPageRecordVersion",
		Fingerprints:     pageRecordFingerprints,
		FingerprintsName: "pageRecordFingerprints",
		Migrations:       pageMigrations,
		MigrationsName:   "pageMigrations",
	})
}

type schemaNormConfig struct {
	StructName       string
	Type             reflect.Type
	CurrentVersion   int
	VersionConstName string
	Fingerprints     map[int]string
	FingerprintsName string
	Migrations       map[int]Migration
	MigrationsName   string
}

func checkSchemaNorm(t *testing.T, c schemaNormConfig) {
	t.Helper()

	got := structFingerprint(c.Type)
	want, ok := c.Fingerprints[c.CurrentVersion]
	if !ok {
		t.Fatalf(`Aucun fingerprint enregistré pour %s à la version %d.
Ajoute-le dans %s, avec cette valeur (copiée depuis ce test) :
%d: %q,`, c.StructName, c.CurrentVersion, c.FingerprintsName, c.CurrentVersion, got)
	}
	if got != want {
		t.Fatalf(`%s a changé de forme sans mise à jour du mécanisme de migration.
Si ce changement est voulu :
  1. Incrémente %s (meta.go).
  2. Ajoute le nouveau fingerprint ci-dessous à %s (schema_norm_test.go).
  3. Enregistre une Migration dans %s (migration.go) avec une Description
     qui explique la conversion depuis la version précédente.
fingerprint actuel : %s
fingerprint attendu (version %d) : %s`,
			c.StructName, c.VersionConstName, c.FingerprintsName, c.MigrationsName, got, c.CurrentVersion, want)
	}

	// Chemin de migration continu de la version 1 (première version du
	// mécanisme) à la version courante.
	for v := 1; v < c.CurrentVersion; v++ {
		if _, ok := c.Migrations[v]; !ok {
			t.Errorf("aucune migration enregistrée de la version %d vers %d pour %s (dans %s)", v, v+1, c.StructName, c.MigrationsName)
		}
	}
}

// TestDocumentRecord_EmbedsRecordMeta et TestPageRecord_EmbedsRecordMeta
// vérifient que le mécanisme d'embedding utilisé pour partager les champs
// communs (SourceHash, SourcePath, DocType, ProcessedAt, SchemaVersion)
// n'a pas été défait par erreur (ex. quelqu'un qui redéclare ces champs à
// plat au lieu d'embedder RecordMeta).
func TestDocumentRecord_EmbedsRecordMeta(t *testing.T) {
	requireEmbedsRecordMeta(t, reflect.TypeOf(DocumentRecord{}))
}

func TestPageRecord_EmbedsRecordMeta(t *testing.T) {
	requireEmbedsRecordMeta(t, reflect.TypeOf(PageRecord{}))
}

func requireEmbedsRecordMeta(t *testing.T, typ reflect.Type) {
	t.Helper()
	f, ok := typ.FieldByName("RecordMeta")
	if !ok || !f.Anonymous || f.Type != reflect.TypeOf(RecordMeta{}) {
		t.Fatalf("%s doit embedder RecordMeta (champ anonyme de type store.RecordMeta) pour partager source_hash/source_path/doc_type/processed_at/schema_version sans les dupliquer", typ.Name())
	}
}
