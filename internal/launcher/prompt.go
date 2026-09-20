package launcher

import (
	"fmt"
	"strings"
)

// ParseOSAScriptTextReturned extrait la valeur "text returned:..." de la
// sortie d'un `osascript -e 'display dialog ... default answer ...'`
// (format documenté d'AppleScript : "button returned:OK, text
// returned:<ce que l'utilisateur a tapé>"). Fonction pure, séparée de
// l'appel réel à osascript (cmd/jarvis-launcher) pour être testable sans
// déclencher une vraie boîte de dialogue.
func ParseOSAScriptTextReturned(output string) (string, error) {
	const marker = "text returned:"
	i := strings.Index(output, marker)
	if i < 0 {
		return "", fmt.Errorf("launcher: sortie osascript inattendue (pas de %q) : %q", marker, output)
	}
	return strings.TrimSpace(output[i+len(marker):]), nil
}
