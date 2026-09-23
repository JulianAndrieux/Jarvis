// Package deploy déploie un ticket accepté (jalon 30) : fusion vérifiée
// dans main, nouveau binaire essayé à blanc, remplacement du processus, et
// retour arrière si la nouvelle version ne démarre pas.
//
// Le marqueur (~/.jarvis/deploy.json) relie les trois acteurs d'un
// déploiement qui survit à un redémarrage : l'application qui déploie,
// la nouvelle version qui le confirme, et le lanceur qui rétablit
// l'ancienne version si la nouvelle s'arrête avant de l'avoir confirmé.
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Marker décrit un déploiement en attente de confirmation.
type Marker struct {
	TicketID string `json:"ticket_id"`
	// Commit : main après la fusion ; PrevCommit : main avant.
	Commit     string `json:"commit"`
	PrevCommit string `json:"prev_commit"`
	// Binary : le binaire lancé (la nouvelle version) ; PrevBinary :
	// l'ancienne version, gardée pour le retour arrière.
	Binary     string    `json:"binary"`
	PrevBinary string    `json:"prev_binary"`
	At         time.Time `json:"at"`
	// RolledBack : le lanceur a rétabli l'ancienne version ; celle-ci,
	// au démarrage, ramène main en arrière et renvoie le ticket en revue.
	RolledBack bool   `json:"rolled_back,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// ReadMarker lit le marqueur ; ok=false s'il n'existe pas.
func ReadMarker(path string) (Marker, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, fmt.Errorf("deploy: lecture du marqueur : %w", err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, false, fmt.Errorf("deploy: marqueur illisible %s : %w", path, err)
	}
	return m, true, nil
}

// WriteMarker écrit le marqueur de façon atomique (fichier temporaire puis
// renommage) : un arrêt en pleine écriture ne laisse jamais un marqueur
// à moitié écrit.
func WriteMarker(path string, m Marker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("deploy: écriture du marqueur : %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("deploy: écriture du marqueur : %w", err)
	}
	return nil
}

// RemoveMarker supprime le marqueur (absent : rien à faire).
func RemoveMarker(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deploy: suppression du marqueur : %w", err)
	}
	return nil
}

// RollbackBinary rétablit l'ancienne version si un déploiement attend sa
// confirmation — appelé par le lanceur quand l'application s'arrête.
// rolled=true : l'appelant doit relancer le binaire. La version fautive
// est gardée en ".failed" pour l'examiner. Une seule fois par
// déploiement : si l'ancienne version tombe à son tour, ce n'est plus un
// problème de déploiement.
func RollbackBinary(markerPath, reason string) (rolled bool, err error) {
	m, ok, err := ReadMarker(markerPath)
	if err != nil || !ok || m.RolledBack {
		return false, err
	}
	if _, err := os.Stat(m.PrevBinary); err != nil {
		return false, fmt.Errorf("deploy: ancienne version introuvable, retour arrière impossible : %w", err)
	}
	if err := os.Rename(m.Binary, m.Binary+".failed"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("deploy: retour arrière : %w", err)
	}
	if err := os.Rename(m.PrevBinary, m.Binary); err != nil {
		return false, fmt.Errorf("deploy: retour arrière : %w", err)
	}
	m.RolledBack, m.Reason = true, reason
	if err := WriteMarker(markerPath, m); err != nil {
		return true, err
	}
	return true, nil
}
