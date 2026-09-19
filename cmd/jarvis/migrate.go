package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/store"
)

// runMigrate implémente `jarvis migrate` : un acte de déploiement
// délibéré qui amène tous les enregistrements persistés sous --out-dir à
// la version courante. Rien n'est migré automatiquement à la lecture
// (cf. store.ReadDocumentRecord/ReadPageRecord et CLAUDE.md) — c'est
// cette commande, et seulement elle, qui réécrit les fichiers.
func runMigrate(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	outDir := fs.String("out-dir", "", "Répertoire de résultats à migrer (celui passé à --out-dir sur `jarvis process`)")
	dryRun := fs.Bool("dry-run", false, "N'écrit rien, affiche seulement ce qui serait migré")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *outDir == "" {
		return fmt.Errorf("--out-dir est requis (le répertoire passé à `jarvis process --out-dir`)")
	}

	hashDirs, err := os.ReadDir(*outDir)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", *outDir, err)
	}

	var migrated, upToDate, failed int
	for _, hd := range hashDirs {
		if !hd.IsDir() {
			continue
		}
		hashDir := filepath.Join(*outDir, hd.Name())

		files, err := os.ReadDir(hashDir)
		if err != nil {
			fmt.Fprintf(stdout, "erreur lecture %s : %v\n", hashDir, err)
			failed++
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			kind, ok := recordKindOf(f.Name())
			if !ok {
				continue // runs.jsonl ou tout autre fichier non concerné
			}

			path := filepath.Join(hashDir, f.Name())
			n, err := migrateOneFile(path, kind, *dryRun, stdout)
			switch {
			case err != nil:
				fmt.Fprintf(stdout, "erreur migration %s : %v\n", path, err)
				failed++
			case n:
				migrated++
			default:
				upToDate++
			}
		}
	}

	fmt.Fprintf(stdout, "%d migré(s), %d déjà à jour, %d erreur(s)\n", migrated, upToDate, failed)
	if failed > 0 {
		return fmt.Errorf("migrate %s: %d fichier(s) en erreur", *outDir, failed)
	}
	return nil
}

type recordKind int

const (
	recordKindDocument recordKind = iota
	recordKindPage
)

func recordKindOf(filename string) (recordKind, bool) {
	switch {
	case filename == "document.json":
		return recordKindDocument, true
	case strings.HasPrefix(filename, "page-") && strings.HasSuffix(filename, ".json"):
		return recordKindPage, true
	default:
		return 0, false
	}
}

// migrateOneFile migre path si besoin. Retourne migrated=true si le
// fichier était obsolète (et a été, ou aurait été en --dry-run, migré).
func migrateOneFile(path string, kind recordKind, dryRun bool, stdout io.Writer) (migrated bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	from, err := store.RecordVersion(raw)
	if err != nil {
		return false, err
	}

	var current int
	var migrateFn func(json.RawMessage) (json.RawMessage, error)
	if kind == recordKindDocument {
		current = store.CurrentDocumentRecordVersion
		migrateFn = store.MigrateDocumentJSON
	} else {
		current = store.CurrentPageRecordVersion
		migrateFn = store.MigratePageJSON
	}

	if from >= current {
		return false, nil
	}

	upgraded, err := migrateFn(raw)
	if err != nil {
		return false, err
	}

	suffix := ""
	if dryRun {
		suffix = " (dry-run, non écrit)"
	}
	fmt.Fprintf(stdout, "migré : %s (v%d -> v%d)%s\n", path, from, current, suffix)

	if dryRun {
		return true, nil
	}

	var indented bytes.Buffer
	if err := json.Indent(&indented, upgraded, "", "  "); err != nil {
		return false, fmt.Errorf("indent migrated JSON: %w", err)
	}
	if err := os.WriteFile(path, indented.Bytes(), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
