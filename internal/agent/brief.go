package agent

import "strings"

// ProjectBrief extrait de CLAUDE.md le contexte utile à l'agent : tout
// ce qui précède "## État des jalons" (contexte, contraintes,
// architecture), borné à maxChars — l'historique des jalons ne tiendrait
// pas dans le contexte du modèle local.
func ProjectBrief(claudeMD string, maxChars int) string {
	if i := strings.Index(claudeMD, "\n## État des jalons"); i >= 0 {
		claudeMD = claudeMD[:i]
	}
	claudeMD = strings.TrimSpace(claudeMD)
	if maxChars > 0 && len(claudeMD) > maxChars {
		claudeMD = truncate(claudeMD, maxChars)
	}
	return claudeMD
}
