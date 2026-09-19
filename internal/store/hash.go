// Package store persiste les résultats du pipeline sur disque : un JSON
// par page (granularité retenue, cf. CLAUDE.md), un résumé par document,
// et un log de rejeu append-only. Chaque enregistrement porte le hash du
// document source, le nom et la version des modèles utilisés, et le
// prompt — la reproductibilité exigée par le brief.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// HashFile calcule le SHA-256 du contenu de path, en hexadécimal. C'est le
// SourceHash journalisé dans chaque enregistrement, pour identifier de
// façon stable le document source (indépendamment de son nom de fichier
// ou de son chemin).
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("store: hash %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("store: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
