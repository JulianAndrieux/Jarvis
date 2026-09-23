package templates

import (
	"fmt"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/JulianAndrieux/Jarvis/internal/projectinfo"
)

// ArchitectureView : la page Architecture, relue à chaque affichage.
type ArchitectureView struct {
	// Fingerprint : empreinte sondée par la page pour se recharger.
	Fingerprint string
	Doc         projectinfo.ProjectDoc
	DocModified time.Time
	Commits     []projectinfo.Commit
	Changes     []projectinfo.Change
	Errors      []string
}

// InfraView : le schéma d'infrastructure après sondage.
type InfraView struct {
	Diagram   projectinfo.Diagram
	SVG       string
	CheckedAt time.Time
}

// decisionOrder : les choix d'abord, le contexte ensuite.
var decisionOrder = []string{
	"Décisions tranchées",
	"Décisions en attente",
	"Contraintes non négociables",
	"Architecture (3 étages strictement séparés, testables isolément)",
	"Non-goals",
	"Process de travail",
	"Contexte",
}

// decisionSections : les sections de CLAUDE.md dans l'ordre de lecture
// de la page ; les autres (ajoutées plus tard) suivent, dans leur ordre.
func decisionSections(d projectinfo.ProjectDoc) []projectinfo.Section {
	var out []projectinfo.Section
	seen := map[string]bool{}
	for _, title := range decisionOrder {
		if s, ok := d.Section(title); ok {
			out = append(out, s)
			seen[title] = true
		}
	}
	for _, s := range d.Sections {
		if !seen[s.Title] {
			out = append(out, s)
		}
	}
	return out
}

func milestonesNewestFirst(d projectinfo.ProjectDoc) []projectinfo.Milestone {
	out := make([]projectinfo.Milestone, len(d.Milestones))
	for i, m := range d.Milestones {
		out[len(out)-1-i] = m
	}
	return out
}

func milestoneCommits(m projectinfo.Milestone, commits []projectinfo.Commit) []projectinfo.Commit {
	var out []projectinfo.Commit
	for _, c := range commits {
		if m.Matches(c.Milestone) {
			out = append(out, c)
		}
	}
	return out
}

func milestoneChip(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "en attente"), strings.Contains(s, "échoue"), strings.Contains(s, "préparé"):
		return "chip chip-review"
	case strings.HasPrefix(s, "fait"), strings.Contains(s, "faits"), strings.Contains(s, "fait,"):
		return "chip chip-done"
	default:
		return "chip"
	}
}

// markdown : texte de CLAUDE.md ou d'un message de commit, rendu échappé.
func markdown(src string) templ.Component {
	return templ.Raw(projectinfo.RenderMarkdown(src))
}

func changeLabel(status string) string {
	switch {
	case status == "??":
		return "nouveau"
	case strings.Contains(status, "D"):
		return "supprimé"
	case strings.Contains(status, "R"):
		return "renommé"
	case strings.Contains(status, "A"):
		return "ajouté"
	default:
		return "modifié"
	}
}

func infraSummary(d projectinfo.Diagram) (string, string) {
	down := 0
	for _, n := range d.Nodes {
		if n.Status.Health == projectinfo.Down {
			down++
		}
	}
	switch down {
	case 0:
		return "Tous les composants sondés répondent", "chip chip-done"
	case 1:
		return "1 composant en panne", "chip chip-failed"
	default:
		return fmt.Sprintf("%d composants en panne", down), "chip chip-failed"
	}
}

func healthLabel(h projectinfo.Health) string {
	switch h {
	case projectinfo.Up:
		return "en service"
	case projectinfo.Down:
		return "en panne"
	case projectinfo.Off:
		return "non configuré"
	default:
		return "non sondé"
	}
}

func healthChip(h projectinfo.Health) string {
	switch h {
	case projectinfo.Up:
		return "chip chip-done"
	case projectinfo.Down:
		return "chip chip-failed"
	default:
		return "chip"
	}
}

func sectionID(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return "s-" + strings.Trim(b.String(), "-")
}
