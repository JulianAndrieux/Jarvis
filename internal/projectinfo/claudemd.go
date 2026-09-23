package projectinfo

import (
	"regexp"
	"strings"
)

// Section est une section de niveau ## de CLAUDE.md (contraintes,
// architecture, décisions...), corps en Markdown.
type Section struct {
	Title string
	Body  string
}

// Milestone est une entrée de "État des jalons".
type Milestone struct {
	// Number : "1", "26-27", "21 bis".
	Number string
	Title  string
	// Status : ce qui suit le titre ("fait, validé end-to-end.").
	Status string
	// Body : le détail en Markdown, désindenté d'un niveau.
	Body string
}

// ProjectDoc est CLAUDE.md découpé pour la page Architecture.
type ProjectDoc struct {
	Sections   []Section
	Milestones []Milestone
	// References : sous-sections ### de l'état des jalons (setup,
	// corpus...), documentation de référence plutôt qu'un jalon.
	References []Section
}

const milestonesSection = "État des jalons"

var milestoneHead = regexp.MustCompile(`^Jalons? (\d+(?:-\d+)?(?: bis)?) — (.*)$`)

// Section retourne la section de titre exact title.
func (d ProjectDoc) Section(title string) (Section, bool) {
	for _, s := range d.Sections {
		if s.Title == title {
			return s, true
		}
	}
	return Section{}, false
}

// ParseProjectDoc découpe le contenu de CLAUDE.md. Tolérant par
// construction : une forme inattendue donne moins d'entrées, jamais une
// erreur (le fichier est tenu à la main).
func ParseProjectDoc(src string) ProjectDoc {
	var d ProjectDoc
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); {
		title, ok := strings.CutPrefix(lines[i], "## ")
		if !ok {
			i++
			continue
		}
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(lines[j], "## ") {
			j++
		}
		body := lines[i+1 : j]
		if title == milestonesSection {
			d.Milestones, d.References = parseMilestones(body)
		} else {
			d.Sections = append(d.Sections, Section{Title: strings.TrimSpace(title), Body: trimBlank(body)})
		}
		i = j
	}
	return d
}

// parseMilestones : chaque entrée commence par "- **Jalon N — ..." (ou
// une autre entrée en gras : correctifs hors jalon) en colonne 0, son détail est indenté ; une ligne non indentée (ou un
// titre ###) le termine.
func parseMilestones(lines []string) ([]Milestone, []Section) {
	var ms []Milestone
	var refs []Section
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "### "):
			j := i + 1
			inFence := false
			for ; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "```") {
					inFence = !inFence
				}
				if !inFence && (strings.HasPrefix(lines[j], "### ") || strings.HasPrefix(lines[j], "- **")) {
					break
				}
			}
			refs = append(refs, Section{Title: strings.TrimSpace(line[4:]), Body: trimBlank(lines[i+1 : j])})
			i = j
		case strings.HasPrefix(line, "- **"):
			// Titre en gras, éventuellement sur plusieurs lignes.
			head := strings.TrimPrefix(line, "- **")
			j := i + 1
			for !strings.Contains(head, "**") && j < len(lines) && strings.HasPrefix(lines[j], "  ") {
				head += " " + strings.TrimSpace(lines[j])
				j++
			}
			bold, rest, _ := strings.Cut(head, "**")
			var body []string
			if r := strings.TrimSpace(rest); r != "" {
				body = append(body, r)
			}
			for ; j < len(lines); j++ {
				l := lines[j]
				if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, " ") {
					break
				}
				body = append(body, strings.TrimPrefix(l, "  "))
			}
			m := Milestone{Body: trimBlank(body)}
			if sm := milestoneHead.FindStringSubmatch(strings.TrimSpace(bold)); sm != nil {
				m.Number = sm[1]
				m.Title, m.Status, _ = strings.Cut(sm[2], " : ")
				m.Title, m.Status = strings.TrimSpace(m.Title), strings.TrimSpace(m.Status)
			} else { // entrée hors jalon numéroté (correctifs...)
				m.Title, m.Status, _ = strings.Cut(strings.TrimSpace(bold), " : ")
			}
			ms = append(ms, m)
			i = j
		default:
			i++
		}
	}
	return ms, refs
}

// Matches dit si le jalon cité par un commit ("Jalons 26-27", "Jalon 26")
// désigne ce jalon.
func (m Milestone) Matches(cited string) bool {
	sm := regexp.MustCompile(`^Jalons? (\d+(?:-\d+)?(?: bis)?)$`).FindStringSubmatch(cited)
	if sm == nil || m.Number == "" {
		return false
	}
	if sm[1] == m.Number {
		return true
	}
	if strings.HasSuffix(sm[1], " bis") || strings.HasSuffix(m.Number, " bis") {
		return false
	}
	lo, hi, _ := strings.Cut(m.Number, "-")
	if hi == "" {
		hi = lo
	}
	c := strings.SplitN(sm[1], "-", 2)[0]
	return atoi(c) >= atoi(lo) && atoi(c) <= atoi(hi)
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func trimBlank(lines []string) string {
	return strings.Trim(strings.Join(lines, "\n"), "\n ")
}
