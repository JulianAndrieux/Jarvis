// Package watch surveille un dossier local et confie chaque nouveau fichier
// trouvé à une fonction de traitement — l'ingestion automatique demandée
// ("un job qui tourne à chaque fois qu'un document est uploadé dans un
// dossier"). Aucune dépendance de notification système (fsnotify...) :
// un simple sondage périodique + déplacement de fichiers suffit et reste
// remplaçable en un jour, cohérent avec "primitives, pas de dépendances
// lourdes".
package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultInterval est l'intervalle de sondage par défaut.
const DefaultInterval = 5 * time.Second

// Watcher sonde Dir à intervalles réguliers pour de nouveaux fichiers
// (de tout type, cf. isDocument) et les confie à OnFile. Aucun état persistant n'est nécessaire
// pour savoir quels fichiers ont déjà été traités : un fichier trouvé
// est immédiatement déplacé hors de Dir (voir processFile), donc un
// fichier encore présent à un sondage est forcément nouveau.
type Watcher struct {
	Dir      string
	Interval time.Duration // 0 -> DefaultInterval
	// OnFile traite un fichier trouvé (nom + contenu). Une erreur envoie
	// le fichier dans Dir/failed/ plutôt que Dir/processed/ — jamais
	// perdu, jamais retraité en boucle indéfiniment.
	OnFile func(ctx context.Context, filename string, content []byte) error
	// Logf, si non-nil, reçoit les messages de progression/erreur.
	Logf func(format string, args ...any)
}

// Run sonde Dir jusqu'à ce que ctx soit annulé — un premier passage a
// lieu immédiatement (pas d'attente du premier Interval), pour traiter
// sans délai les fichiers déjà présents au démarrage.
func (w *Watcher) Run(ctx context.Context) error {
	interval := w.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		w.logf("watch: read dir %s: %v", w.Dir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !isDocument(e.Name()) {
			continue
		}
		w.processFile(ctx, e.Name())
	}
}

// isDocument écarte ce qui n'est pas (encore) un document à ingérer :
// fichiers cachés (.DS_Store, verrous LibreOffice ".~lock..."), verrous
// d'Office ("~$..."), téléchargements ou copies en cours. Tout le reste
// est ingéré, quel que soit son type (jalon 25).
func isDocument(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".crdownload", ".part", ".download", ".tmp":
		return false
	}
	return true
}

// processFile déplace name hors de Dir avant même de le lire (protège
// contre un double traitement si un fichier est encore en cours
// d'écriture ou si deux sondages se chevauchent), puis le confie à
// OnFile et range le résultat dans processed/ ou failed/.
func (w *Watcher) processFile(ctx context.Context, name string) {
	claimDir := filepath.Join(w.Dir, ".processing")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		w.logf("watch: create %s: %v", claimDir, err)
		return
	}
	claimed := filepath.Join(claimDir, name)
	if err := os.Rename(filepath.Join(w.Dir, name), claimed); err != nil {
		// Déjà réclamé par un passage précédent (ou disparu) — rien à faire.
		return
	}

	content, err := os.ReadFile(claimed)
	if err != nil {
		w.logf("watch: read %s: %v", name, err)
		w.moveTo(claimed, name, "failed")
		return
	}

	if err := w.OnFile(ctx, name, content); err != nil {
		w.logf("watch: process %s: %v", name, err)
		w.moveTo(claimed, name, "failed")
		return
	}

	w.moveTo(claimed, name, "processed")
}

// moveTo déplace from vers Dir/subdir/name, sans jamais écraser un
// fichier existant du même nom (suffixe l'horodatage si besoin) — un
// nom de fichier réutilisé (même document redéposé plus tard) ne doit
// jamais effacer silencieusement une trace précédente.
func (w *Watcher) moveTo(from, name, subdir string) {
	dir := filepath.Join(w.Dir, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.logf("watch: create %s: %v", dir, err)
		return
	}

	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		dest = filepath.Join(dir, fmt.Sprintf("%s-%s%s", base, strconv.FormatInt(time.Now().UnixNano(), 10), ext))
	}

	if err := os.Rename(from, dest); err != nil {
		w.logf("watch: move %s to %s: %v", from, dest, err)
	}
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}
