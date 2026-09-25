package launcher

import (
	"path/filepath"
	"strings"
)

// toolDirs : emplacements usuels des outils dont Jarvis a besoin sur macOS
// — Homebrew (llama-server, soffice, pdftoppm), Go (navigateur de code) ;
// plus home/go/bin et home/.local/bin (Claude Code).
var toolDirs = []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/go/bin"}

// WithToolPaths complète path avec les dossiers d'outils absents (et
// home/go/bin), en gardant les entrées existantes en tête. Un .app lancé
// depuis le Finder reçoit un PATH minimal sans Homebrew ni Go.
func WithToolPaths(path, home string) string {
	parts := []string{}
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			parts = append(parts, p)
		}
	}
	for _, p := range strings.Split(path, ":") {
		add(p)
	}
	for _, p := range toolDirs {
		add(p)
	}
	if home != "" {
		add(filepath.Join(home, "go", "bin"))
		// Claude Code (agent des tickets, jalon 41) s'installe là.
		add(filepath.Join(home, ".local", "bin"))
	}
	return strings.Join(parts, ":")
}
