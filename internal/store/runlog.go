package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunLogEntry est une ligne du journal de rejeu d'un document : un résumé
// d'une exécution du pipeline sur ce document. Contrairement à
// document.json/page-N.json (écrasés à chaque run), runs.jsonl est
// append-only : il garde l'historique des tentatives, utile pour
// comprendre pourquoi un champ est sorti faux après coup (ex. changement
// de modèle ou de prompt entre deux runs).
type RunLogEntry struct {
	Timestamp          time.Time `json:"timestamp"`
	SourceHash         string    `json:"source_hash"`
	SourcePath         string    `json:"source_path"`
	DocType            string    `json:"doc_type"`
	PagesTotal         int       `json:"pages_total"`
	PagesNeedingReview int       `json:"pages_needing_review"`
	PagesFailed        int       `json:"pages_failed"`
}

// AppendRunLog ajoute entry à dir/<hash>/runs.jsonl (une ligne JSON par
// appel, le fichier est créé si besoin).
func AppendRunLog(dir, hash string, entry RunLogEntry) error {
	docDir := filepath.Join(dir, hash)
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		return fmt.Errorf("store: create %s: %w", docDir, err)
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal run log entry: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(docDir, "runs.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("store: open runs.jsonl: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("store: append run log entry: %w", err)
	}
	return nil
}
