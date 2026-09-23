package projectinfo

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// RenderMarkdown rend le sous-ensemble de Markdown utilisé dans CLAUDE.md
// (listes imbriquées, listes numérotées, titres ###, blocs de code, gras,
// code, barré) en HTML. Tout le texte est échappé : aucune balise de la
// source ne passe.
func RenderMarkdown(src string) string {
	var b strings.Builder
	r := mdRenderer{b: &b}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case strings.HasPrefix(trimmed, "```"):
			r.closeAll()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, strings.TrimPrefix(lines[i], strings.Repeat(" ", indent)))
			}
			b.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>")
		case trimmed == "":
			r.closePara()
		case strings.HasPrefix(trimmed, "#"):
			r.closeAll()
			b.WriteString("<h4>" + inline(strings.TrimLeft(trimmed, "# ")) + "</h4>")
		default:
			if tag, text, ok := listItem(trimmed); ok {
				r.item(indent, tag, text)
			} else if len(r.stack) > 0 && (indent > 0 || !r.blank) {
				r.text = append(r.text, trimmed) // suite de l'élément courant
			} else {
				if len(r.stack) > 0 {
					r.closeAll()
				}
				r.para = append(r.para, trimmed)
			}
		}
		r.blank = trimmed == ""
	}
	r.closeAll()
	return b.String()
}

var orderedMarker = regexp.MustCompile(`^\d+[.)] `)

func listItem(s string) (tag, text string, ok bool) {
	if rest, found := strings.CutPrefix(s, "- "); found {
		return "ul", rest, true
	}
	if rest, found := strings.CutPrefix(s, "* "); found {
		return "ul", rest, true
	}
	if m := orderedMarker.FindString(s); m != "" {
		return "ol", s[len(m):], true
	}
	return "", "", false
}

type listLevel struct {
	indent int
	tag    string
}

type mdRenderer struct {
	b     *strings.Builder
	stack []listLevel
	text  []string // élément de liste en cours, pas encore écrit
	para  []string
	blank bool
}

func (r *mdRenderer) flushText() {
	if len(r.text) > 0 {
		r.b.WriteString(inline(strings.Join(r.text, " ")))
		r.text = nil
	}
}

func (r *mdRenderer) item(indent int, tag, text string) {
	r.closePara()
	r.flushText()
	// Remonter jusqu'au niveau de cet élément.
	for len(r.stack) > 0 && indent < r.stack[len(r.stack)-1].indent {
		r.b.WriteString("</li></" + r.stack[len(r.stack)-1].tag + ">")
		r.stack = r.stack[:len(r.stack)-1]
	}
	top := len(r.stack) - 1
	switch {
	case top >= 0 && indent == r.stack[top].indent && tag == r.stack[top].tag:
		r.b.WriteString("</li>")
	case top >= 0 && indent == r.stack[top].indent: // autre type de liste au même niveau
		r.b.WriteString("</li></" + r.stack[top].tag + "><" + tag + ">")
		r.stack[top].tag = tag
	default: // nouveau niveau (liste imbriquée dans le <li> ouvert)
		r.b.WriteString("<" + tag + ">")
		r.stack = append(r.stack, listLevel{indent: indent, tag: tag})
	}
	r.b.WriteString("<li>")
	r.text = []string{text}
}

func (r *mdRenderer) closePara() {
	if len(r.para) > 0 {
		r.b.WriteString("<p>" + inline(strings.Join(r.para, " ")) + "</p>")
		r.para = nil
	}
}

func (r *mdRenderer) closeAll() {
	r.flushText()
	for len(r.stack) > 0 {
		r.b.WriteString("</li></" + r.stack[len(r.stack)-1].tag + ">")
		r.stack = r.stack[:len(r.stack)-1]
	}
	r.closePara()
}

var (
	boldRe   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	strikeRe = regexp.MustCompile(`~~(.+?)~~`)
)

// inline : code entre accents graves (contenu protégé), gras, barré. Le
// code est mis de côté avant le reste : un passage en gras peut en
// contenir.
func inline(s string) string {
	var codes []string
	var b strings.Builder
	parts := strings.Split(s, "`")
	for i, p := range parts {
		switch {
		case i%2 == 1 && i < len(parts)-1:
			fmt.Fprintf(&b, "\x00%d\x00", len(codes))
			codes = append(codes, "<code>"+html.EscapeString(p)+"</code>")
		case i%2 == 1: // accent grave non refermé
			b.WriteString(html.EscapeString("`" + p))
		default:
			b.WriteString(html.EscapeString(p))
		}
	}
	out := boldRe.ReplaceAllString(b.String(), "<strong>$1</strong>")
	out = strikeRe.ReplaceAllString(out, "<del>$1</del>")
	for i, c := range codes {
		out = strings.Replace(out, fmt.Sprintf("\x00%d\x00", i), c, 1)
	}
	return out
}
