package notes

import (
	"context"
	"strings"
	"testing"
	"time"
)

// storeContract : ce que tout Store doit respecter — FakeStore (tests
// unitaires) et MongoStore (intégration, Atlas) passent le même contrat.
// stamp rend les identifiants et les recherches uniques (collection de
// test partagée).
func storeContract(t *testing.T, s Store, stamp string) {
	ctx := context.Background()
	at := func(m int) time.Time { return time.Date(2026, 9, 23, 10, m, 0, 0, time.UTC) }
	id := func(n string) string { return stamp + "-" + n }

	// Notes : aller-retour, mise à jour, suppression.
	n := Note{ID: id("n1"), Title: "Courses " + stamp, Body: "- lait\n- **pain**", Tags: []string{"maison"}, DocIDs: []string{"doc-1"}, CreatedAt: at(0), UpdatedAt: at(0)}
	if err := s.CreateNote(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetNote(ctx, n.ID)
	if err != nil || !ok || got.Title != n.Title || got.Body != n.Body || len(got.Tags) != 1 || len(got.DocIDs) != 1 || !got.CreatedAt.Equal(n.CreatedAt) {
		t.Fatalf("GetNote = %+v, %v, %v", got, ok, err)
	}
	n.Body, n.Pinned, n.UpdatedAt = "modifié", true, at(5)
	if err := s.UpdateNote(ctx, n); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetNote(ctx, n.ID); got.Body != "modifié" || !got.Pinned {
		t.Errorf("after update = %+v", got)
	}
	if _, ok, err := s.GetNote(ctx, id("absente")); ok || err != nil {
		t.Errorf("GetNote(unknown) = %v, %v", ok, err)
	}
	if err := s.UpdateNote(ctx, Note{ID: id("absente")}); err == nil {
		t.Error("UpdateNote(unknown) = nil")
	}

	// Liste : épinglées d'abord, puis la plus récemment modifiée ;
	// recherche insensible à la casse sur titre, texte et tags.
	for _, x := range []Note{
		{ID: id("n2"), Title: "Idées " + stamp, Body: "Voyage au JAPON", UpdatedAt: at(9), CreatedAt: at(1)},
		{ID: id("n3"), Title: "Réunion " + stamp, Tags: []string{"travail"}, UpdatedAt: at(7), CreatedAt: at(2), DocIDs: []string{"doc-2"}},
	} {
		if err := s.CreateNote(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ns []Note) string {
		var out []string
		for _, x := range ns {
			out = append(out, strings.TrimPrefix(x.ID, stamp+"-"))
		}
		return strings.Join(out, ",")
	}
	all, err := s.ListNotes(ctx, NoteQuery{Search: stamp})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(all); got != "n1,n2,n3" {
		t.Errorf("ListNotes order = %s, want n1 (épinglée), n2, n3", got)
	}
	for q, want := range map[NoteQuery]string{
		{Search: "japon"}:               "n2",
		{Search: stamp, Tag: "travail"}: "n3",
		{Search: "MAISON"}:              "n1", // un tag
		{Search: stamp, DocID: "doc-2"}: "n3",
	} {
		ns, err := s.ListNotes(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var mine []Note
		for _, x := range ns {
			if strings.HasPrefix(x.ID, stamp) {
				mine = append(mine, x)
			}
		}
		if got := ids(mine); got != want {
			t.Errorf("ListNotes(%+v) = %s, want %s", q, got, want)
		}
	}

	// Tâches : aller-retour, filtres par note et document.
	tasks := []Task{
		{ID: id("t1"), Title: "Acheter du lait", Due: "2026-09-24", Priority: High, NoteID: n.ID, CreatedAt: at(0)},
		{ID: id("t2"), Title: "Relire le devis", DocID: "doc-2", CreatedAt: at(1)},
	}
	for _, x := range tasks {
		if err := s.CreateTask(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	tk, ok, err := s.GetTask(ctx, id("t1"))
	if err != nil || !ok || tk.Due != "2026-09-24" || tk.Priority != High || tk.NoteID != n.ID {
		t.Fatalf("GetTask = %+v, %v, %v", tk, ok, err)
	}
	tk.Done, tk.DoneAt = true, at(8)
	if err := s.UpdateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if tk, _, _ := s.GetTask(ctx, id("t1")); !tk.Done || !tk.DoneAt.Equal(at(8)) {
		t.Errorf("after update = %+v", tk)
	}
	byNote, _ := s.ListTasks(ctx, TaskQuery{NoteID: n.ID})
	byDoc, _ := s.ListTasks(ctx, TaskQuery{DocID: "doc-2"})
	if len(byNote) != 1 || byNote[0].ID != id("t1") || len(byDoc) < 1 {
		t.Errorf("ListTasks by note = %+v, by doc = %+v", byNote, byDoc)
	}
	if err := s.UpdateTask(ctx, Task{ID: id("absente")}); err == nil {
		t.Error("UpdateTask(unknown) = nil")
	}

	// Suppression.
	for _, del := range []func() error{
		func() error { return s.DeleteNote(ctx, id("n1")) },
		func() error { return s.DeleteNote(ctx, id("n2")) },
		func() error { return s.DeleteNote(ctx, id("n3")) },
		func() error { return s.DeleteTask(ctx, id("t1")) },
		func() error { return s.DeleteTask(ctx, id("t2")) },
	} {
		if err := del(); err != nil {
			t.Error(err)
		}
	}
	if _, ok, _ := s.GetNote(ctx, id("n1")); ok {
		t.Error("note still there after delete")
	}
	if err := s.DeleteTask(ctx, id("t1")); err == nil {
		t.Error("DeleteTask(unknown) = nil")
	}
}

func TestFakeStore_Contract(t *testing.T) {
	storeContract(t, NewFakeStore(), "fake")
}
