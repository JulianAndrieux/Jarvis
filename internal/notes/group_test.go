package notes

import (
	"testing"
	"time"
)

func TestGroupTasks(t *testing.T) {
	created := func(m int) time.Time { return wednesday.Add(time.Duration(m) * time.Minute) }
	tasks := []Task{
		{ID: "retard", Due: "2026-09-20", CreatedAt: created(1)},
		{ID: "auj-normale", Due: "2026-09-23", CreatedAt: created(2)},
		{ID: "auj-haute", Due: "2026-09-23", Priority: High, CreatedAt: created(3)},
		{ID: "loin", Due: "2026-10-10", CreatedAt: created(4)},
		{ID: "bientot", Due: "2026-09-25", CreatedAt: created(5)},
		{ID: "sans", CreatedAt: created(6)},
		{ID: "sans-basse", Priority: Low, CreatedAt: created(0)},
		{ID: "faite", Due: "2026-09-01", Done: true, DoneAt: created(7)},
		{ID: "faite-avant", Done: true, DoneAt: created(1)},
	}
	groups := GroupTasks(tasks, wednesday)
	want := []struct {
		bucket Bucket
		ids    []string
	}{
		{Overdue, []string{"retard"}},
		{Today, []string{"auj-haute", "auj-normale"}}, // priorité d'abord
		{Upcoming, []string{"bientot", "loin"}},       // puis échéance
		{NoDue, []string{"sans", "sans-basse"}},       // puis priorité avant création
		{Done, []string{"faite", "faite-avant"}},      // terminées : la plus récente d'abord
	}
	if len(groups) != len(want) {
		t.Fatalf("groups = %+v", groups)
	}
	for i, w := range want {
		g := groups[i]
		if g.Bucket != w.bucket || len(g.Tasks) != len(w.ids) {
			t.Fatalf("group %d = %s %d tasks, want %s %v", i, g.Bucket, len(g.Tasks), w.bucket, w.ids)
		}
		for j, id := range w.ids {
			if g.Tasks[j].ID != id {
				t.Errorf("group %s[%d] = %s, want %s", g.Bucket, j, g.Tasks[j].ID, id)
			}
		}
	}
	if Overdue.Label() == "" || Done.Label() == "" {
		t.Error("buckets need a label")
	}
}

// Un groupe vide n'est pas retourné.
func TestGroupTasks_SkipsEmptyGroups(t *testing.T) {
	groups := GroupTasks([]Task{{ID: "a"}}, wednesday)
	if len(groups) != 1 || groups[0].Bucket != NoDue {
		t.Errorf("groups = %+v", groups)
	}
}

func TestDueLabel(t *testing.T) {
	for due, want := range map[string]string{
		"":           "",
		"2026-09-23": "aujourd'hui",
		"2026-09-24": "demain",
		"2026-09-22": "hier",
		"2026-09-28": "lundi", // dans la semaine
		"2026-09-30": "30/09", // une semaine ou plus
		"2026-09-10": "10/09",
		"2027-01-05": "05/01/2027", // autre année
		"n'importe":  "n'importe",
	} {
		if got := DueLabel(due, wednesday); got != want {
			t.Errorf("DueLabel(%q) = %q, want %q", due, got, want)
		}
	}
}

// Changements d'heure (Paris : journées de 25 h en octobre, 23 h en
// mars) : le libellé ne se décale pas d'un jour.
func TestDueLabel_AcrossDaylightSaving(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("fuseau Europe/Paris indisponible")
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, paris) // samedi
	for due, want := range map[string]string{"2026-10-25": "demain", "2026-10-26": "lundi", "2026-10-31": "31/10"} {
		if got := DueLabel(due, now); got != want {
			t.Errorf("DueLabel(%q) = %q, want %q", due, got, want)
		}
	}
	// 29 mars 2026 : journée de 23 h — une division tronquée donnait
	// « demain » pour le lundi 30.
	spring := time.Date(2026, 3, 28, 12, 0, 0, 0, paris) // samedi
	for due, want := range map[string]string{"2026-03-29": "demain", "2026-03-30": "lundi"} {
		if got := DueLabel(due, spring); got != want {
			t.Errorf("DueLabel(%q) au printemps = %q, want %q", due, got, want)
		}
	}
}
