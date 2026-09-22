package templates

import (
	"fmt"
	"html"
	"strings"

	"github.com/a-h/templ"

	"github.com/JulianAndrieux/Jarvis/internal/highlight"
)

// CodeView est un extrait de code Go coloré, prêt à afficher (jalon 24).
type CodeView struct {
	Lines     [][]highlight.Token
	FirstLine int    // numéro de la première ligne dans le fichier
	File      string // relatif à la racine du module
}

// codeHTML produit le HTML du bloc de code. Écrit en Go plutôt qu'en
// templ : templ insère des espaces entre éléments en ligne, ce qui
// altérerait le code dans un <pre>. Tout texte est échappé ; les numéros
// de ligne sont en CSS (::before) pour ne pas être copiés avec le code.
func codeHTML(c CodeView) string {
	var b strings.Builder
	b.WriteString(`<pre class="code"><code>`)
	for i, line := range c.Lines {
		fmt.Fprintf(&b, `<span class="code-line" data-ln="%d">`, c.FirstLine+i)
		for _, tok := range line {
			// Tabulations -> 4 espaces : dans un <pre>, une tabulation
			// s'aligne sur le taquet suivant, décalé par la gouttière des
			// numéros de ligne (l'indentation devenait un seul espace).
			text := html.EscapeString(strings.ReplaceAll(tok.Text, "\t", "    "))
			if tok.Class == highlight.Plain {
				b.WriteString(text)
				continue
			}
			fmt.Fprintf(&b, `<span class="tok-%s">%s</span>`, tok.Class, text)
		}
		b.WriteString("</span>\n")
	}
	b.WriteString(`</code></pre>`)
	return b.String()
}

func codeLocation(c CodeView) string {
	return fmt.Sprintf("%s:%d", c.File, c.FirstLine)
}

func rawCode(c CodeView) templ.Component {
	return templ.Raw(codeHTML(c))
}
