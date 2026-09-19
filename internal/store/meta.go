package store

import "time"

// RecordMeta regroupe les champs communs à tous les enregistrements
// persistés (DocumentRecord, PageRecord). Embedded plutôt que dupliqué —
// c'est l'équivalent Go de l'héritage pour cet usage : composition +
// promotion des champs. encoding/json aplatit les champs d'un embedding
// anonyme sans tag propre, donc le format JSON sur disque est inchangé.
//
// SchemaVersion est l'analogue, par fichier, de ce qu'on stockerait dans
// une base partagée (ex. future collection Atlas) pour savoir quelle
// version du code a produit/mis à jour un enregistrement — voir
// CLAUDE.md, section "Migrations de structs persistées".
type RecordMeta struct {
	SourceHash    string    `json:"source_hash"`
	SourcePath    string    `json:"source_path"`
	DocType       string    `json:"doc_type"`
	ProcessedAt   time.Time `json:"processed_at"`
	SchemaVersion int       `json:"schema_version"`
}

// CurrentDocumentRecordVersion et CurrentPageRecordVersion sont les
// versions courantes des structs correspondants. Toute modification de
// leur forme (ajout/suppression/renommage/retypage d'un champ exporté)
// DOIT s'accompagner de :
//  1. l'incrément de la constante de version correspondante ;
//  2. l'ajout du nouveau fingerprint dans documentRecordFingerprints/
//     pageRecordFingerprints (schema_norm_test.go) ;
//  3. l'enregistrement d'une Migration (migration.go) décrivant la
//     conversion depuis la version précédente.
//
// TestDocumentRecordSchema_ChangeRequiresMigration et
// TestPageRecordSchema_ChangeRequiresMigration font échouer la suite de
// tests si ces trois étapes ne sont pas faites ensemble.
const (
	CurrentDocumentRecordVersion = 2
	CurrentPageRecordVersion     = 1
)
