package email

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// PlainText retourne une représentation texte complète pour l'indexation et
// la conversion en PDF : en-têtes utiles (De, À, Cc, Date, Objet) puis le
// corps — Text s'il existe, sinon HTML converti en texte (HTMLToText) —
// puis la liste des pièces jointes (nom + taille). Les en-têtes vides sont
// omis plutôt qu'affichés sans valeur.
func (m Message) PlainText() string {
	var b strings.Builder
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s : %s\n", label, value)
		}
	}
	line("De", formatAddresses(m.From))
	line("À", formatAddresses(m.To))
	line("Cc", formatAddresses(m.Cc))
	if !m.Date.IsZero() {
		line("Date", m.Date.Format("02/01/2006 15:04 -0700"))
	}
	line("Objet", m.Subject)

	body := strings.TrimRight(strings.TrimLeft(m.Text, "\r\n"), " \t\r\n")
	if body == "" {
		body = HTMLToText(m.HTML)
	}
	if body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}

	if len(m.Attachments) > 0 {
		b.WriteString("\nPièces jointes :\n")
		for _, a := range m.Attachments {
			name := a.Filename
			if name == "" {
				name = "(sans nom)"
			}
			fmt.Fprintf(&b, "- %s (%s, %s)\n", name, a.ContentType, formatSize(a.Size))
		}
	}
	return b.String()
}

func formatAddresses(list []Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		switch {
		case a.Name != "" && a.Email != "":
			parts = append(parts, a.Name+" <"+a.Email+">")
		case a.Email != "":
			parts = append(parts, a.Email)
		case a.Name != "":
			parts = append(parts, a.Name)
		}
	}
	return strings.Join(parts, ", ")
}

// formatSize affiche une taille à la française (virgule décimale, Ko/Mo).
func formatSize(n int) string {
	switch {
	case n < 1024:
		if n <= 1 {
			return fmt.Sprintf("%d octet", n)
		}
		return fmt.Sprintf("%d octets", n)
	case n < 1024*1024:
		return strings.Replace(fmt.Sprintf("%.1f Ko", float64(n)/1024), ".", ",", 1)
	default:
		return strings.Replace(fmt.Sprintf("%.1f Mo", float64(n)/(1024*1024)), ".", ",", 1)
	}
}

// attrs reconnaît le contenu d'une balise en respectant les valeurs
// d'attribut entre guillemets, qui peuvent contenir ">".
const attrs = `(?:[^>"']|"[^"]*"|'[^']*')*`

var (
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// RE2 n'a pas de référence arrière : une expression par élément dont
	// le contenu doit disparaître avec les balises.
	reDropped = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b` + attrs + `>.*?</script\s*>`),
		regexp.MustCompile(`(?is)<style\b` + attrs + `>.*?</style\s*>`),
		regexp.MustCompile(`(?is)<head\b` + attrs + `>.*?</head\s*>`),
		regexp.MustCompile(`(?is)<title\b` + attrs + `>.*?</title\s*>`),
	}
	// Un script ou style jamais fermé avale tout le reste, comme dans un
	// navigateur : mieux vaut perdre la fin que de livrer du JavaScript.
	reUnclosed   = regexp.MustCompile(`(?is)<(?:script|style)\b.*$`)
	reSpaces     = regexp.MustCompile(`[ \t\r\n\f]+`)
	reBr         = regexp.MustCompile(`(?i)<br\b` + attrs + `>`)
	reBlockOpen  = regexp.MustCompile(`(?i)<(?:p|h[1-6]|table|ul|ol|blockquote|pre)\b` + attrs + `>`)
	reBlockClose = regexp.MustCompile(`(?i)</(?:p|div|tr|li|h[1-6]|table|ul|ol|blockquote|pre|section|article|header|footer|dt|dd)\s*>`)
	reCellClose  = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	reTag        = regexp.MustCompile(`<[a-zA-Z/!?]` + attrs + `>`)
	reInlineWS   = regexp.MustCompile(`[ \t]+`)
	reManyBlank  = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText convertit du HTML en texte lisible : retire script/style/head,
// convertit <br>, </p>, </div>, </tr>, </li> en retours à la ligne, décode
// les entités HTML, réduit les lignes vides multiples.
//
// Conversion par expressions régulières (bibliothèque standard seule,
// golang.org/x/net/html n'étant pas une dépendance du module) : suffisante
// pour du HTML d'e-mail, dont on veut le texte, pas la structure exacte.
// L'ordre compte : les blancs du source sont réduits avant d'insérer les
// sauts de ligne, et les entités décodées en dernier pour que "&lt;b&gt;"
// reste du texte au lieu de devenir une balise supprimée.
func HTMLToText(s string) string {
	if s == "" {
		return ""
	}
	s = reComment.ReplaceAllString(s, "")
	for _, re := range reDropped {
		s = re.ReplaceAllString(s, "")
	}
	s = reUnclosed.ReplaceAllString(s, "")
	s = reSpaces.ReplaceAllString(s, " ") // en HTML, les sauts de ligne du source ne comptent pas
	s = reBr.ReplaceAllString(s, "\n")
	s = reBlockOpen.ReplaceAllString(s, "\n")
	s = reBlockClose.ReplaceAllString(s, "\n")
	s = reCellClose.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ") // &nbsp;

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reInlineWS.ReplaceAllString(l, " "))
	}
	s = strings.Join(lines, "\n")
	s = reManyBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
