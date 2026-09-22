package templates

import (
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/highlight"
)

// Une tabulation dans un <pre> s'aligne sur le taquet suivant — décalé
// par la gouttière des numéros de ligne, l'indentation devenait un seul
// espace. Tabulations converties en 4 espaces (affichage par défaut de
// VS Code ; gofmt aligne avec des espaces, rien d'autre n'est perdu).
func TestCodeHTML_TabsBecomeFourSpaces(t *testing.T) {
	c := CodeView{FirstLine: 1, Lines: highlight.Lines(highlight.Go("func f() {\n\tif x {\n\t\treturn\n\t}\n}", highlight.Options{}))}
	out := codeHTML(c)
	if strings.Contains(out, "\t") {
		t.Errorf("code HTML still contains tabs: %q", out)
	}
	if !strings.Contains(out, `data-ln="3">        <span class="tok-ctl">return</span>`) {
		t.Errorf("line 3 should be indented by 8 spaces: %q", out)
	}
}

func TestCodeHTML_EscapesSourceText(t *testing.T) {
	c := CodeView{FirstLine: 1, Lines: highlight.Lines(highlight.Go(`var s = "<script>"`, highlight.Options{}))}
	if out := codeHTML(c); strings.Contains(out, "<script>") {
		t.Errorf("source text must be escaped: %q", out)
	}
}
