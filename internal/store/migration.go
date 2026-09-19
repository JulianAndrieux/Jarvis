package store

import (
	"encoding/json"
	"fmt"
)

// Migration décrit comment convertir un enregistrement de sa version
// (la clé dans documentMigrations/pageMigrations) vers la version
// suivante. Description est obligatoire (voir
// TestMigrations_HaveNonEmptyDescriptions) : c'est le "quoi faire" que le
// brief demande pour tout changement de struct persistée — un lecteur
// humain doit pouvoir comprendre la conversion sans lire Apply.
type Migration struct {
	Description string
	Apply       func(map[string]any) (map[string]any, error)
}

// documentMigrations et pageMigrations sont les registres — le mécanisme
// qui "lie des fonctions à une struct" : chaque changement de forme de
// DocumentRecord/PageRecord s'accompagne d'une entrée ici, à la clé de la
// version de départ.
var documentMigrations = map[int]Migration{
	// 0 -> 1 : introduction même de RecordMeta.SchemaVersion. Les
	// enregistrements écrits avant ce mécanisme n'ont pas de champ
	// schema_version (donc recordVersion les traite comme version 0) et
	// n'ont besoin d'aucune autre conversion.
	0: {
		Description: "Introduction de schema_version (RecordMeta). Aucune autre transformation : les enregistrements existants n'avaient pas ce champ, il est simplement ajouté.",
		Apply:       identityMigration,
	},
	// 1 -> 2 : ajout de MergedExtraction (jalon 11, finding 3 du jalon
	// 10 : les champs d'un document dont les valeurs sont réparties sur
	// plusieurs pages n'étaient reliés par aucun enregistrement unique).
	// Purement additif : un enregistrement v1 n'a pas la clé
	// "merged_extraction", ce qui décode naturellement en nil (le champ
	// est *PageExtraction, omitempty) — aucune transformation de données
	// existantes n'est nécessaire, seule la version doit avancer.
	1: {
		Description: "Ajout de MergedExtraction (fusion des extractions de toutes les pages, meilleure confiance par champ — voir extraction.MergePages). Aucune autre transformation : absent d'un enregistrement v1, le champ décode simplement à nil.",
		Apply:       identityMigration,
	},
}

var pageMigrations = map[int]Migration{
	0: {
		Description: "Introduction de schema_version (RecordMeta). Aucune autre transformation : les enregistrements existants n'avaient pas ce champ, il est simplement ajouté.",
		Apply:       identityMigration,
	},
}

func identityMigration(m map[string]any) (map[string]any, error) {
	return m, nil
}

// recordVersion lit "schema_version" dans un enregistrement décodé ;
// absent (enregistrement écrit avant l'introduction de ce champ) vaut 0.
func recordVersion(m map[string]any) int {
	v, ok := m["schema_version"]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int(f)
}

// RecordVersion lit le champ schema_version d'un enregistrement JSON brut
// (0 si absent — enregistrement écrit avant l'introduction de ce champ).
// Utilisée par `jarvis migrate` pour décider si un fichier a besoin
// d'être migré, sans dépendre d'un struct de destination particulier.
func RecordVersion(raw json.RawMessage) (int, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, fmt.Errorf("store: decode record: %w", err)
	}
	return recordVersion(m), nil
}

// MigrateDocumentJSON et MigratePageJSON appliquent en séquence les
// migrations enregistrées pour amener un enregistrement à la version
// courante. Utilisées par la commande `jarvis migrate` (acte de
// déploiement délibéré, cf. CLAUDE.md) — pas appelées automatiquement à
// chaque lecture : ReadDocumentRecord/ReadPageRecord refusent un
// enregistrement obsolète plutôt que de le migrer silencieusement.
func MigrateDocumentJSON(raw json.RawMessage) (json.RawMessage, error) {
	return migrateJSON(raw, documentMigrations, CurrentDocumentRecordVersion)
}

func MigratePageJSON(raw json.RawMessage) (json.RawMessage, error) {
	return migrateJSON(raw, pageMigrations, CurrentPageRecordVersion)
}

func migrateJSON(raw json.RawMessage, migrations map[int]Migration, currentVersion int) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("store: decode record for migration: %w", err)
	}

	version := recordVersion(m)
	for version < currentVersion {
		mig, ok := migrations[version]
		if !ok {
			return nil, fmt.Errorf("store: no migration registered from version %d to %d", version, version+1)
		}
		var err error
		m, err = mig.Apply(m)
		if err != nil {
			return nil, fmt.Errorf("store: migration %d->%d: %w", version, version+1, err)
		}
		version++
		m["schema_version"] = version
	}

	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("store: marshal migrated record: %w", err)
	}
	return out, nil
}
