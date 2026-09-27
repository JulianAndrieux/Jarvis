package notes

import (
	"context"
	"strings"
	"testing"
	"time"
)

// texts : le texte de chaque boîte de la note, dans l'ordre.
func texts(t *testing.T, store *FakeStore, id string) string {
	t.Helper()
	n, ok, err := store.GetNote(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetNote(%s) = %v, %v", id, ok, err)
	}
	var out []string
	for _, b := range n.Blocks {
		out = append(out, b.Text)
	}
	return strings.Join(out, "|")
}

func TestNewNote_HasOneEmptyBox(t *testing.T) {
	s, _, _ := newTestService()
	n, err := s.NewNote(context.Background(), "")
	if err != nil || len(n.Blocks) != 1 || n.Blocks[0].Text != "" || n.Blocks[0].ID == "" {
		t.Fatalf("NewNote = %+v, %v", n, err)
	}
}

func TestAddBlock_InsertsAfterTheGivenBox(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	first := n.Blocks[0].ID
	s.SaveBlock(ctx, n.ID, first, "un", "")

	b2, err := s.AddBlock(ctx, n.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	s.SaveBlock(ctx, n.ID, b2.ID, "trois", "")
	// Insérée après la première : elle se glisse entre les deux.
	b3, err := s.AddBlock(ctx, n.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	s.SaveBlock(ctx, n.ID, b3.ID, "deux", "")
	if got := texts(t, store, n.ID); got != "un|deux|trois" {
		t.Errorf("après insertion = %q", got)
	}
	// Une boîte inconnue : à la fin, pas une erreur.
	b4, err := s.AddBlock(ctx, n.ID, "inconnue")
	if err != nil {
		t.Fatal(err)
	}
	s.SaveBlock(ctx, n.ID, b4.ID, "quatre", "")
	if got := texts(t, store, n.ID); got != "un|deux|trois|quatre" {
		t.Errorf("après ajout en fin = %q", got)
	}
}

func TestSaveBlock_UpdatesTextTagsAndDerivedBody(t *testing.T) {
	s, store, now := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	*now = now.Add(time.Hour)
	b, err := s.SaveBlock(ctx, n.ID, n.Blocks[0].ID, "## Courses", " maison, urgent ,, maison ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.Tags, "|") != "maison|urgent" || !b.UpdatedAt.Equal(*now) {
		t.Errorf("SaveBlock = %+v", b)
	}
	got, _, _ := store.GetNote(ctx, n.ID)
	if got.Body != "## Courses" {
		t.Errorf("Body dérivé = %q", got.Body)
	}
	if !got.UpdatedAt.Equal(*now) {
		t.Errorf("Note.UpdatedAt = %v, want %v", got.UpdatedAt, *now)
	}
	if _, err := s.SaveBlock(ctx, n.ID, "inconnue", "x", ""); err == nil {
		t.Error("SaveBlock(boîte inconnue) = nil")
	}
}

// Une note d'avant les boîtes : son corps devient une boîte à la
// première écriture, sans rien perdre.
func TestSaveBlock_OnALegacyNoteConvertsItsBodyIntoOneBox(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	store.CreateNote(ctx, Note{ID: "old", Title: "Ancienne", Body: "texte hérité", CreatedAt: wednesday, UpdatedAt: wednesday})

	b, err := s.AddBlock(ctx, "old", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveBlock(ctx, "old", b.ID, "ajout", ""); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, store, "old"); got != "texte hérité|ajout" {
		t.Errorf("boîtes = %q", got)
	}
	if got, _, _ := store.GetNote(ctx, "old"); got.Body != "texte hérité\n\najout" {
		t.Errorf("Body = %q", got.Body)
	}
}

func TestMoveBlock_UpDownAndBounds(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	ids := []string{n.Blocks[0].ID}
	s.SaveBlock(ctx, n.ID, ids[0], "un", "")
	for _, txt := range []string{"deux", "trois"} {
		b, _ := s.AddBlock(ctx, n.ID, "")
		s.SaveBlock(ctx, n.ID, b.ID, txt, "")
		ids = append(ids, b.ID)
	}
	if err := s.MoveBlock(ctx, n.ID, ids[2], true); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, store, n.ID); got != "un|trois|deux" {
		t.Errorf("après ↑ = %q", got)
	}
	if err := s.MoveBlock(ctx, n.ID, ids[0], false); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, store, n.ID); got != "trois|un|deux" {
		t.Errorf("après ↓ = %q", got)
	}
	// Aux bornes : rien ne bouge, sans erreur.
	if err := s.MoveBlock(ctx, n.ID, ids[2], true); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveBlock(ctx, n.ID, ids[1], false); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, store, n.ID); got != "trois|un|deux" {
		t.Errorf("aux bornes = %q", got)
	}
	if err := s.MoveBlock(ctx, n.ID, "inconnue", true); err == nil {
		t.Error("MoveBlock(boîte inconnue) = nil")
	}
}

// Une note a toujours au moins une boîte : supprimer la dernière en
// laisse une vide.
func TestDeleteBlock_KeepsAtLeastOneEmptyBox(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	s.SaveBlock(ctx, n.ID, n.Blocks[0].ID, "un", "")
	b2, _ := s.AddBlock(ctx, n.ID, "")
	s.SaveBlock(ctx, n.ID, b2.ID, "deux", "")

	if err := s.DeleteBlock(ctx, n.ID, n.Blocks[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, store, n.ID); got != "deux" {
		t.Errorf("après suppression = %q", got)
	}
	if err := s.DeleteBlock(ctx, n.ID, b2.ID); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.GetNote(ctx, n.ID)
	if len(got.Blocks) != 1 || got.Blocks[0].Text != "" || got.Body != "" {
		t.Errorf("après la dernière suppression = %+v, body %q", got.Blocks, got.Body)
	}
	if err := s.DeleteBlock(ctx, n.ID, "inconnue"); err == nil {
		t.Error("DeleteBlock(boîte inconnue) = nil")
	}
}

func TestBlockToTask_UsesQuickAddOnTheFirstLine(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	bid := n.Blocks[0].ID
	s.SaveBlock(ctx, n.ID, bid, "- Appeler le notaire demain !\nDétails en dessous", "")

	task, err := s.BlockToTask(ctx, n.ID, bid)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Appeler le notaire" || task.Due != isoDate(wednesday.AddDate(0, 0, 1)) || task.Priority != High || task.NoteID != n.ID {
		t.Fatalf("tâche = %+v", task)
	}
	got, _, _ := store.GetNote(ctx, n.ID)
	if len(got.Blocks) != 1 || got.Blocks[0].TaskID != task.ID {
		t.Errorf("la boîte ne mémorise pas sa tâche : %+v", got.Blocks)
	}
	if got.Blocks[0].Text == "" {
		t.Error("la boîte a disparu ; la note doit rester la trace")
	}
}

func TestBlockToTask_RefusesEmptyBoxAndSecondConversion(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	bid := n.Blocks[0].ID
	if _, err := s.BlockToTask(ctx, n.ID, bid); err == nil {
		t.Fatal("BlockToTask(boîte vide) = nil")
	}
	tasks, _ := store.ListTasks(ctx, TaskQuery{})
	if len(tasks) != 0 {
		t.Fatalf("une tâche a été créée depuis une boîte vide : %+v", tasks)
	}

	s.SaveBlock(ctx, n.ID, bid, "Relire le devis", "")
	task, err := s.BlockToTask(ctx, n.ID, bid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BlockToTask(ctx, n.ID, bid); err == nil {
		t.Error("deuxième conversion = nil, want erreur")
	}
	// La tâche supprimée entre-temps : la boîte peut redevenir une tâche.
	if err := s.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.BlockToTask(ctx, n.ID, bid)
	if err != nil || again.ID == task.ID {
		t.Errorf("reconversion = %+v, %v", again, err)
	}
}

func TestArchiveBlock_RequiresATag(t *testing.T) {
	s, store, now := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	bid := n.Blocks[0].ID
	s.SaveBlock(ctx, n.ID, bid, "vieille idée", "")

	if _, err := s.ArchiveBlock(ctx, n.ID, bid, ""); err == nil {
		t.Fatal("archiver sans tag = nil, want erreur")
	}
	if got, _, _ := store.GetNote(ctx, n.ID); got.Blocks[0].Archived {
		t.Fatal("la boîte a été archivée malgré le refus")
	}
	*now = now.Add(time.Hour)
	b, err := s.ArchiveBlock(ctx, n.ID, bid, "archive, 2025")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Archived || !b.ArchivedAt.Equal(*now) || strings.Join(b.Tags, "|") != "archive|2025" {
		t.Errorf("ArchiveBlock = %+v", b)
	}
	// Déjà taguée : ses tags suffisent, pas besoin d'en redonner.
	b2, _ := s.AddBlock(ctx, n.ID, "")
	s.SaveBlock(ctx, n.ID, b2.ID, "autre", "vieux")
	if b, err := s.ArchiveBlock(ctx, n.ID, b2.ID, ""); err != nil || !b.Archived || strings.Join(b.Tags, "|") != "vieux" {
		t.Errorf("archiver une boîte déjà taguée = %+v, %v", b, err)
	}
}

func TestUnarchiveBlock_PutsItBack(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	bid := n.Blocks[0].ID
	s.SaveBlock(ctx, n.ID, bid, "vieille idée", "")
	s.ArchiveBlock(ctx, n.ID, bid, "archive")

	b, err := s.UnarchiveBlock(ctx, n.ID, bid)
	if err != nil || b.Archived || !b.ArchivedAt.IsZero() || strings.Join(b.Tags, "|") != "archive" {
		t.Fatalf("UnarchiveBlock = %+v, %v", b, err)
	}
	if got, _, _ := store.GetNote(ctx, n.ID); got.Blocks[0].Archived {
		t.Error("la boîte est restée archivée en base")
	}
}

func TestNewMailNote_PutsItsTextInOneBox(t *testing.T) {
	s, _, _ := newTestService()
	n, err := s.NewMailNote(context.Background(), "mail-1", "Facture Acme", "> Bonjour")
	if err != nil || len(n.Blocks) != 1 || n.Blocks[0].Text != "> Bonjour" || n.Body != "> Bonjour" {
		t.Fatalf("NewMailNote = %+v, %v", n, err)
	}
}

// L'en-tête d'une note (titre, tags, épingle) ne touche pas aux boîtes.
func TestSaveNote_LeavesTheBoxesAlone(t *testing.T) {
	s, store, _ := newTestService()
	ctx := context.Background()
	n, _ := s.NewNote(ctx, "")
	s.SaveBlock(ctx, n.ID, n.Blocks[0].ID, "à garder", "")
	if _, err := s.SaveNote(ctx, n.ID, "Courses", "maison", true); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.GetNote(ctx, n.ID)
	if len(got.Blocks) != 1 || got.Blocks[0].Text != "à garder" || got.Body != "à garder" {
		t.Errorf("après SaveNote = %+v, body %q", got.Blocks, got.Body)
	}
	if got.Title != "Courses" || !got.Pinned {
		t.Errorf("en-tête = %+v", got)
	}
}
