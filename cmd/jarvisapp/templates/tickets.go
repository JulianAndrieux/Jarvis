package templates

import (
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
	case tickets.Analyzing:
		return "chip chip-running"
	case tickets.PlanReady:
		return "chip chip-review"
	case tickets.PlanApproved:
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
