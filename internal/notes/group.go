package notes

import (
	"math"
	"sort"
	"time"
)

// Bucket : les groupes de la todo.
type Bucket string

const (
	Overdue  Bucket = "en_retard"
	Today    Bucket = "aujourdhui"
	Upcoming Bucket = "a_venir"
	NoDue    Bucket = "sans_echeance"
	Done     Bucket = "terminees"
)

// Label : titre du groupe.
func (b Bucket) Label() string {
	switch b {
	case Overdue:
		return "En retard"
	case Today:
		return "Aujourd'hui"
	case Upcoming:
		return "À venir"
	case NoDue:
		return "Sans échéance"
	case Done:
		return "Terminées"
	}
	return string(b)
}

// Group : les tâches d'un groupe, dans l'ordre d'affichage.
type Group struct {
	Bucket Bucket
	Tasks  []Task
}

// BucketOf : le groupe d'une tâche à la date now.
func BucketOf(t Task, now time.Time) Bucket {
	today := DateOf(now)
	switch {
	case t.Done:
		return Done
	case t.Due == "":
		return NoDue
	case t.Due < today:
		return Overdue
	case t.Due == today:
		return Today
	}
	return Upcoming
}

// GroupTasks répartit les tâches par groupe (En retard, Aujourd'hui, À
// venir, Sans échéance, Terminées ; groupes vides omis). Dans un groupe :
// la priorité la plus haute, puis l'échéance la plus proche, puis la plus
// ancienne ; les terminées, la plus récemment terminée d'abord.
func GroupTasks(tasks []Task, now time.Time) []Group {
	byBucket := map[Bucket][]Task{}
	for _, t := range tasks {
		b := BucketOf(t, now)
		byBucket[b] = append(byBucket[b], t)
	}
	var out []Group
	for _, b := range []Bucket{Overdue, Today, Upcoming, NoDue, Done} {
		ts := byBucket[b]
		if len(ts) == 0 {
			continue
		}
		sort.SliceStable(ts, func(i, j int) bool {
			if b == Done {
				return ts[i].DoneAt.After(ts[j].DoneAt)
			}
			if ri, rj := ts[i].Priority.rank(), ts[j].Priority.rank(); ri != rj {
				return ri < rj
			}
			if ts[i].Due != ts[j].Due {
				return ts[i].Due < ts[j].Due
			}
			return ts[i].CreatedAt.Before(ts[j].CreatedAt)
		})
		out = append(out, Group{Bucket: b, Tasks: ts})
	}
	return out
}

var weekdayNames = [...]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"}

// DueLabel : une échéance lisible — aujourd'hui, demain, hier, le jour de
// la semaine s'il est dans les 6 prochains jours, sinon JJ/MM (JJ/MM/AAAA
// pour une autre année).
func DueLabel(due string, now time.Time) string {
	d, err := time.ParseInLocation("2006-01-02", due, now.Location())
	if err != nil {
		return due
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// Arrondi : une journée de changement d'heure dure 23 ou 25 h.
	switch days := int(math.Round(d.Sub(today).Hours() / 24)); {
	case days == 0:
		return "aujourd'hui"
	case days == 1:
		return "demain"
	case days == -1:
		return "hier"
	case days > 1 && days < 7:
		return weekdayNames[d.Weekday()]
	}
	if d.Year() != today.Year() {
		return d.Format("02/01/2006")
	}
	return d.Format("02/01")
}

// Clock : l'heure du service (celle des tests si Now est fixé).
func (s *Service) Clock() time.Time { return s.now() }
