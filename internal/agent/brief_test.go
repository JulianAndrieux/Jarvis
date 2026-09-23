package agent

import (
	"strings"
	"testing"
)

// Le contexte donné au modèle : le début de CLAUDE.md (contexte,
// contraintes, architecture), pas l'historique des jalons — des
// centaines de lignes qui ne tiendraient pas dans 8192 jetons.
func TestProjectBrief_StopsBeforeMilestoneHistoryAndIsBounded(t *testing.T) {
	doc := "# Jarvis\n\n## Contraintes non négociables\n- TDD strict\n\n## État des jalons\n- Jalon 1 ...\n"
	brief := ProjectBrief(doc, 1000)
	if !strings.Contains(brief, "TDD strict") || strings.Contains(brief, "Jalon 1") {
		t.Errorf("brief = %q", brief)
	}
	long := "# X\n" + strings.Repeat("ligne de contexte\n", 500)
	if got := ProjectBrief(long, 1000); len(got) > 1100 {
		t.Errorf("brief length = %d, want it bounded near 1000", len(got))
	}
}
