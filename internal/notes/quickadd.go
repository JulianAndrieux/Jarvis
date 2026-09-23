package notes

import (
	"strconv"
	"strings"
	"time"
)

// QuickTask : ce qu'une saisie rapide décrit.
type QuickTask struct {
	Title    string
	Due      string // "AAAA-MM-JJ" ou ""
	Priority Priority
}

// ParseQuickAdd lit une saisie rapide de tâche. Les mots-clés reconnus
// en fin de saisie donnent l'échéance — aujourd'hui, demain,
// après-demain, un jour de la semaine (le prochain, jamais aujourd'hui),
// JJ/MM (l'an prochain si la date est passée) ou JJ/MM/AAAA — et la
// priorité : « ! » ou « !haute », « !basse ». Seulement en fin de
// saisie : « Préparer demain la réunion » garde son titre. Un mot-clé
// seul reste le titre.
func ParseQuickAdd(input string, today time.Time) QuickTask {
	words := strings.Fields(input)
	q := QuickTask{}
	for len(words) > 1 {
		last := words[len(words)-1]
		if p, ok := priorityWord(last); ok && q.Priority == Normal {
			q.Priority = p
		} else if d, ok := dateWord(last, today); ok && q.Due == "" {
			q.Due = d
		} else {
			break
		}
		words = words[:len(words)-1]
	}
	q.Title = strings.Join(words, " ")
	return q
}

func priorityWord(w string) (Priority, bool) {
	switch strings.ToLower(w) {
	case "!", "!!", "!haute":
		return High, true
	case "!basse":
		return Low, true
	}
	return Normal, false
}

var weekdays = map[string]time.Weekday{
	"lundi": time.Monday, "mardi": time.Tuesday, "mercredi": time.Wednesday,
	"jeudi": time.Thursday, "vendredi": time.Friday, "samedi": time.Saturday, "dimanche": time.Sunday,
}

func dateWord(w string, today time.Time) (string, bool) {
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	lw := strings.ToLower(strings.ReplaceAll(w, "’", "'"))
	switch lw {
	case "aujourd'hui", "auj":
		return isoDate(day), true
	case "demain":
		return isoDate(day.AddDate(0, 0, 1)), true
	case "après-demain", "apres-demain":
		return isoDate(day.AddDate(0, 0, 2)), true
	}
	if wd, ok := weekdays[lw]; ok {
		n := (int(wd) - int(day.Weekday()) + 7) % 7
		if n == 0 {
			n = 7
		}
		return isoDate(day.AddDate(0, 0, n)), true
	}
	parts := strings.Split(lw, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return "", false
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", false
		}
		nums[i] = n
	}
	year := day.Year()
	if len(nums) == 3 {
		year = nums[2]
	}
	d, ok := validDate(year, nums[1], nums[0], day.Location())
	if !ok {
		return "", false
	}
	if len(nums) == 2 && d.Before(day) {
		if d, ok = validDate(year+1, nums[1], nums[0], day.Location()); !ok {
			return "", false
		}
	}
	return isoDate(d), true
}

// validDate refuse les dates impossibles (31/02) que time.Date
// normaliserait en silence.
func validDate(year, month, day int, loc *time.Location) (time.Time, bool) {
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, loc)
	return d, d.Year() == year && int(d.Month()) == month && d.Day() == day
}

func isoDate(t time.Time) string { return t.Format("2006-01-02") }

// DateOf : la date de now, au format des échéances.
func DateOf(now time.Time) string { return isoDate(now) }
