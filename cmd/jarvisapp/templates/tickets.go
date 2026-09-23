package templates

import (
	"fmt"
	"html"
	"strings"

	"github.com/a-h/templ"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// renderPlan met en forme le plan de l'agent : sortie d'un modèle, donc
// non fiable — tout le texte est échappé, seuls les titres Markdown
// ("## ...") deviennent des <h4>, le reste garde ses retours à la ligne.
func renderPlan(plan string) templ.Component {
	var b strings.Builder
	var para []string
	flush := func() {
		if len(para) > 0 {
			b.WriteString(`<div class="plan-text">` + html.EscapeString(strings.Join(para, "\n")) + `</div>`)
			para = nil
		}
	}
	for _, line := range strings.Split(plan, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			flush()
			b.WriteString("<h4>" + html.EscapeString(strings.TrimSpace(strings.TrimLeft(trimmed, "#"))) + "</h4>")
			continue
		}
		para = append(para, line)
	}
	flush()
	return templ.Raw(b.String())
}

func ticketChipClass(s tickets.Status) string {
	switch s {
	case tickets.Analyzing, tickets.Developing:
		return "chip chip-running"
	case tickets.PlanReady, tickets.Review:
		return "chip chip-review"
	case tickets.PlanApproved, tickets.Accepted:
		return "chip chip-done"
	case tickets.Failed:
		return "chip chip-failed"
	default:
		return "chip"
	}
}

func eventIcon(e tickets.Event) string {
	switch e.Kind {
	case tickets.EventStep:
		return "🔎"
	case tickets.EventPlan:
		return "📝"
	case tickets.EventError:
		return "⚠️"
	case tickets.EventComment:
		return "💬"
	default:
		return "•"
	}
}

// renderDiff affiche un diff unifié par fichier, lignes colorées. Le diff
// contient du code écrit par un modèle : tout est échappé. Construit en
// Go, comme les blocs de code (templ insérerait des espaces dans le
// <pre>).
func renderDiff(diff string) templ.Component {
	var b strings.Builder
	open := false
	closeFile := func() {
		if open {
			b.WriteString("</pre></div>")
			open = false
		}
	}
	files := 0
	for _, line := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			closeFile()
			name := line
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				name = line[i+3:]
			}
			fmt.Fprintf(&b, `<div class="diff-file"><div class="diff-name">%s</div><pre class="diff">`, html.EscapeString(name))
			open = true
			files++
			continue
		}
		if !open {
			continue
		}
		class := "diff-ctx"
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file"), strings.HasPrefix(line, "deleted file"):
			class = "diff-meta"
		case strings.HasPrefix(line, "@@"):
			class = "diff-hunk"
		case strings.HasPrefix(line, "+"):
			class = "diff-add"
		case strings.HasPrefix(line, "-"):
			class = "diff-del"
		}
		fmt.Fprintf(&b, `<span class="diff-line %s">%s</span>`+"\n", class, html.EscapeString(strings.ReplaceAll(line, "\t", "    ")))
	}
	closeFile()
	if files == 0 {
		return templ.Raw(`<p class="muted">Aucune modification.</p>`)
	}
	return templ.Raw(b.String())
}
