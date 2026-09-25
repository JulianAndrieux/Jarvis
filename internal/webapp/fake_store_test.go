package webapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

func TestFakeStore_CreateThenGet(t *testing.T) {
	s := NewFakeStore()
	job := Job{ID: "1", Filename: "doc.pdf", Status: StatusPending}

	created, err := s.Create(context.Background(), job)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if created.ID != "1" {
		t.Errorf("created.ID = %q, want 1", created.ID)
	}

	got, ok, err := s.Get(context.Background(), "1")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got.Filename != "doc.pdf" {
		t.Errorf("got.Filename = %q, want doc.pdf", got.Filename)
	}
}

func TestFakeStore_Get_UnknownID_ReturnsFalse(t *testing.T) {
	s := NewFakeStore()

	_, ok, err := s.Get(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if ok {
		t.Error("Get() ok = true, want false")
	}
}

func TestFakeStore_Update_ExistingJob_Overwrites(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	if _, err := s.Create(ctx, Job{ID: "1", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, Job{ID: "1", Status: StatusDone}); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}

	got, ok, err := s.Get(ctx, "1")
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	if got.Status != StatusDone {
		t.Errorf("got.Status = %q, want done", got.Status)
	}
}

func TestFakeStore_Update_UnknownJob_ReturnsError(t *testing.T) {
	s := NewFakeStore()

	err := s.Update(context.Background(), Job{ID: "does-not-exist"})
	if err == nil {
		t.Fatal("Update() error = nil, want non-nil for a job that was never created")
	}
}

func TestFakeStore_List_SortedByCreatedAtDescending(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	base := time.Now()
	_, _ = s.Create(ctx, Job{ID: "old", CreatedAt: base})
	_, _ = s.Create(ctx, Job{ID: "newest", CreatedAt: base.Add(2 * time.Hour)})
	_, _ = s.Create(ctx, Job{ID: "middle", CreatedAt: base.Add(1 * time.Hour)})

	got, err := s.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 3 || got[0].ID != "newest" || got[1].ID != "middle" || got[2].ID != "old" {
		ids := make([]string, len(got))
		for i, j := range got {
			ids[i] = j.ID
		}
		t.Errorf("List() ids = %v, want [newest middle old]", ids)
	}
}

func TestFakeStore_List_FiltersBySearchOnFilenameDocTypeAndTags_CaseInsensitive(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	_, _ = s.Create(ctx, Job{ID: "1", Filename: "Facture-Acme.pdf", DocType: "facture"})
	_, _ = s.Create(ctx, Job{ID: "2", Filename: "rapport.pdf", DocType: "piece_identite", Tags: []string{"Urgent"}})
	_, _ = s.Create(ctx, Job{ID: "3", Filename: "autre.pdf", DocType: "facture"})

	byFilename, err := s.List(ctx, ListQuery{Search: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byFilename) != 1 || byFilename[0].ID != "1" {
		t.Errorf("search by filename = %+v, want just job 1", byFilename)
	}

	byTag, err := s.List(ctx, ListQuery{Search: "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTag) != 1 || byTag[0].ID != "2" {
		t.Errorf("search by tag = %+v, want just job 2", byTag)
	}

	byDocType, err := s.List(ctx, ListQuery{Search: "facture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byDocType) != 2 {
		t.Errorf("search by doc_type = %+v, want 2 matches", byDocType)
	}
}

func TestFakeStore_List_FiltersBySearchOnSearchText(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	_, _ = s.Create(ctx, Job{ID: "1", Filename: "doc1.pdf", SearchText: "Contient le mot Kangourou dans le texte"})
	_, _ = s.Create(ctx, Job{ID: "2", Filename: "doc2.pdf", SearchText: "Rien à voir"})

	got, err := s.List(ctx, ListQuery{Search: "kangourou"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("search by content = %+v, want just job 1", got)
	}
}

func TestFakeStore_List_FiltersByExactStatus(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	_, _ = s.Create(ctx, Job{ID: "1", Status: StatusRunning})
	_, _ = s.Create(ctx, Job{ID: "2", Status: StatusDone})
	_, _ = s.Create(ctx, Job{ID: "3", Status: StatusPending})

	got, err := s.List(ctx, ListQuery{Status: StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("List(Status=running) = %+v, want just job 1", got)
	}
}

func TestFakeStore_Delete_RemovesJob(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	if _, err := s.Create(ctx, Job{ID: "1", Filename: "doc.pdf"}); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}

	_, ok, err := s.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Get() ok = true after Delete(), want false")
	}
}

func TestFakeStore_Delete_UnknownID_ReturnsError(t *testing.T) {
	s := NewFakeStore()
	err := s.Delete(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("Delete() error = nil, want non-nil for an unknown job id")
	}
}

func TestFakeStore_List_RespectsLimit(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = s.Create(ctx, Job{ID: string(rune('a' + i)), CreatedAt: time.Now()})
	}

	got, err := s.List(ctx, ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len(List()) = %d, want 2 (Limit)", len(got))
	}
}

// Jalon 22 : miniature de la première page, pour la grille de la
// bibliothèque de documents.
func TestFakeStore_SetThumbnail_PersistsOnJob(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	if _, err := s.Create(ctx, Job{ID: "a", Filename: "a.pdf"}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetThumbnail(ctx, "a", []byte("png")); err != nil {
		t.Fatalf("SetThumbnail() error = %v", err)
	}
	got, _, _ := s.Get(ctx, "a")
	if string(got.Thumbnail) != "png" {
		t.Errorf("Thumbnail = %q, want %q", got.Thumbnail, "png")
	}
}

func TestFakeStore_SetThumbnail_UnknownJob_ReturnsError(t *testing.T) {
	if err := NewFakeStore().SetThumbnail(context.Background(), "nope", []byte("png")); err == nil {
		t.Error("SetThumbnail() error = nil, want an error for an unknown job")
	}
}

// SummaryOnly (liste de la bibliothèque) ne renvoie ni le PDF, ni le
// résultat, ni la miniature : la Fake les retire elle aussi, pour qu'un
// appelant qui en dépendrait par erreur échoue dès les tests unitaires.
func TestFakeStore_List_SummaryOnly_OmitsHeavyFields(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a", Filename: "a.pdf", Content: []byte("%PDF"), Thumbnail: []byte("png"), Result: &pipeline.Result{DocType: "facture"}, DocType: "facture"})

	got, err := s.List(ctx, ListQuery{SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("List() = %d jobs, want 1", len(got))
	}
	if got[0].Content != nil || got[0].Thumbnail != nil || got[0].Result != nil {
		t.Errorf("List(SummaryOnly) kept heavy fields: content=%d thumb=%d result=%v", len(got[0].Content), len(got[0].Thumbnail), got[0].Result)
	}
	if got[0].DocType != "facture" || got[0].Filename != "a.pdf" {
		t.Errorf("List(SummaryOnly) = %+v, want summary fields kept", got[0])
	}

	full, _ := s.List(ctx, ListQuery{})
	if full[0].Result == nil {
		t.Error("List() without SummaryOnly must still return full jobs (RecoverOrphaned relies on it)")
	}
}

// Jalon 23 : l'avancement (texte déjà lu) est écrit par SetProgress,
// jamais par Update — comme la miniature.
func TestFakeStore_SetProgress_PersistsAndClears(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a"})

	p := &pipeline.Progress{Stage: pipeline.StageParsing, ParseDone: 1, ParseTotal: 5}
	if err := s.SetProgress(ctx, "a", p); err != nil {
		t.Fatalf("SetProgress() error = %v", err)
	}
	got, _, _ := s.Get(ctx, "a")
	if got.Progress == nil || got.Progress.ParseDone != 1 {
		t.Errorf("Progress = %+v, want ParseDone 1", got.Progress)
	}

	if err := s.SetProgress(ctx, "a", nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "a")
	if got.Progress != nil {
		t.Errorf("Progress = %+v, want nil after clearing", got.Progress)
	}
	if err := s.SetProgress(ctx, "nope", p); err == nil {
		t.Error("SetProgress() on unknown job: error = nil, want an error")
	}
}

// La Fake doit se comporter comme MongoStore : Update ne touche ni à la
// miniature ni à l'avancement (écrits par leurs propres méthodes). Sinon
// un Update depuis un Job en mémoire les effacerait dans les tests mais
// pas en production — et un bug réel passerait inaperçu.
func TestFakeStore_Update_PreservesThumbnailAndProgress(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a", Status: StatusRunning})
	s.SetThumbnail(ctx, "a", []byte("png"))
	s.SetProgress(ctx, "a", &pipeline.Progress{ParseDone: 2})

	if err := s.Update(ctx, Job{ID: "a", Status: StatusFailed}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(ctx, "a")
	if string(got.Thumbnail) != "png" || got.Progress == nil || got.Progress.ParseDone != 2 {
		t.Errorf("after Update: thumbnail=%q progress=%+v, want both preserved", got.Thumbnail, got.Progress)
	}
	if got.Status != StatusFailed {
		t.Errorf("Status = %s, want failed (Update must still apply its own fields)", got.Status)
	}
}

func TestFakeStore_List_SummaryOnly_OmitsProgress(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a"})
	s.SetProgress(ctx, "a", &pipeline.Progress{ParseDone: 1})

	got, _ := s.List(ctx, ListQuery{SummaryOnly: true})
	if got[0].Progress != nil {
		t.Errorf("List(SummaryOnly).Progress = %+v, want nil", got[0].Progress)
	}
}

// --- Jalon 25 : fichiers séparés des métadonnées ---

func TestFakeStore_CreateMovesContentToFilesAndGetStaysLight(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	if _, err := s.Create(ctx, Job{ID: "a", Filename: "a.docx", Content: []byte("docx-bytes")}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(ctx, "a")
	if got.Content != nil {
		t.Errorf("Get().Content = %q, want nil (read on demand with ReadFile)", got.Content)
	}
	data, ok, err := s.ReadFile(ctx, "a", FileOriginal)
	if err != nil || !ok || string(data) != "docx-bytes" {
		t.Errorf("ReadFile(original) = %q ok=%v err=%v", data, ok, err)
	}
}

func TestFakeStore_WriteReadDeleteFiles(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a", Content: []byte("x")})

	if _, ok, _ := s.ReadFile(ctx, "a", FileRendition); ok {
		t.Error("rendition should not exist yet")
	}
	s.WriteFile(ctx, "a", FileRendition, []byte("v1"))
	s.WriteFile(ctx, "a", FileRendition, []byte("v2"))
	if data, _, _ := s.ReadFile(ctx, "a", FileRendition); string(data) != "v2" {
		t.Errorf("rendition = %q, want v2", data)
	}
	if err := s.WriteFile(ctx, "nope", FileRendition, []byte("x")); err == nil {
		t.Error("WriteFile on unknown job: error = nil")
	}
	s.Delete(ctx, "a")
	if _, ok, _ := s.ReadFile(ctx, "a", FileOriginal); ok {
		t.Error("files should be gone after Delete")
	}
}

func TestFakeStore_List_FiltersByFormat(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a", Filename: "a.xlsx", Format: "sheet"})
	s.Create(ctx, Job{ID: "b", Filename: "b.pdf", Format: "pdf"})
	s.Create(ctx, Job{ID: "c", Filename: "c.pdf"}) // antérieur au jalon 25 : un PDF

	got, _ := s.List(ctx, ListQuery{Format: "pdf"})
	if len(got) != 2 {
		t.Errorf("List(Format=pdf) = %d jobs, want 2 (including the legacy one without Format)", len(got))
	}
	if got, _ := s.List(ctx, ListQuery{Format: "sheet"}); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("List(Format=sheet) = %+v", got)
	}
}

// Ticket "Ajouter commentaire sur document" : les mots du commentaire
// sont trouvés par la barre de recherche.
func TestFakeStore_List_FiltersBySearchOnComment(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	_, _ = s.Create(ctx, Job{ID: "1", Filename: "a.pdf", Comment: "À rappeler au Notaire avant lundi"})
	_, _ = s.Create(ctx, Job{ID: "2", Filename: "b.pdf"})

	got, err := s.List(ctx, ListQuery{Search: "notaire"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("search by comment = %+v, want just job 1", got)
	}
}

// Ticket "Ajouter un filtre sur les documents" : filtre par date
// d'import, intervalle semi-ouvert [CreatedFrom, CreatedBefore) — une
// borne nulle ne filtre pas.
func TestFakeStore_List_FiltersByCreationDate(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	day := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.Local) }
	_, _ = s.Create(ctx, Job{ID: "avant", CreatedAt: day(9, 23)})
	_, _ = s.Create(ctx, Job{ID: "debut", CreatedAt: day(10, 0)})
	_, _ = s.Create(ctx, Job{ID: "fin", CreatedAt: day(12, 23)})
	_, _ = s.Create(ctx, Job{ID: "apres", CreatedAt: day(13, 0)})

	ids := func(q ListQuery) string {
		got, err := s.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, j := range got {
			out = append(out, j.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(ListQuery{CreatedFrom: day(10, 0), CreatedBefore: day(13, 0)}); got != "fin,debut" {
		t.Errorf("10 → 12 septembre = %s, want fin,debut", got)
	}
	if got := ids(ListQuery{CreatedFrom: day(12, 0)}); got != "apres,fin" {
		t.Errorf("depuis le 12 = %s", got)
	}
	if got := ids(ListQuery{CreatedBefore: day(10, 0)}); got != "avant" {
		t.Errorf("avant le 10 = %s", got)
	}
}

// Count compte sans charger : mêmes filtres que List, mais Limit ignoré
// (le tableau de bord veut le total, pas une page de résultats).
func TestFakeStore_Count_MatchesListFiltersAndIgnoresLimit(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 10, 0, 0, 0, time.Local) }
	for _, j := range []Job{
		{ID: "a", Filename: "facture-a.pdf", Status: StatusDone, CreatedAt: day(10)},
		{ID: "b", Filename: "facture-b.pdf", Status: StatusDone, CreatedAt: day(11)},
		{ID: "c", Filename: "devis.pdf", Status: StatusDone, CreatedAt: day(12)},
		{ID: "d", Filename: "cassé.docx", Status: StatusFailed, Format: "word", CreatedAt: day(13)},
		{ID: "e", Filename: "compta.xlsx", Status: StatusRunning, Format: "sheet", CreatedAt: day(14)},
	} {
		if _, err := s.Create(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	count := func(q ListQuery) int {
		n, err := s.Count(ctx, q)
		if err != nil {
			t.Fatalf("Count(%+v) error = %v", q, err)
		}
		return n
	}
	// Le même filtre doit donner le même compte que List : Count et List
	// partagent la même condition, jamais deux copies qui divergent.
	sameAsList := func(q ListQuery) {
		t.Helper()
		got, err := s.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if n := count(q); n != len(got) {
			t.Errorf("Count(%+v) = %d, want %d (comme List)", q, n, len(got))
		}
	}

	if n := count(ListQuery{}); n != 5 {
		t.Errorf("Count() = %d, want 5", n)
	}
	if n := count(ListQuery{Status: StatusFailed}); n != 1 {
		t.Errorf("Count(Status=failed) = %d, want 1", n)
	}
	if n := count(ListQuery{Status: StatusDone}); n != 3 {
		t.Errorf("Count(Status=done) = %d, want 3", n)
	}
	if n := count(ListQuery{Format: "sheet"}); n != 1 {
		t.Errorf("Count(Format=sheet) = %d, want 1", n)
	}
	if n := count(ListQuery{Search: "facture"}); n != 2 {
		t.Errorf("Count(Search=facture) = %d, want 2", n)
	}
	sameAsList(ListQuery{Search: "facture"})
	sameAsList(ListQuery{CreatedFrom: day(12), CreatedBefore: day(14)})

	// Limit borne List, jamais Count.
	if n := count(ListQuery{Limit: 2}); n != 5 {
		t.Errorf("Count(Limit=2) = %d, want 5 (Limit ignoré)", n)
	}
	if got, _ := s.List(ctx, ListQuery{Limit: 2}); len(got) != 2 {
		t.Errorf("List(Limit=2) = %d jobs, want 2 (la prémisse du test)", len(got))
	}
}

// Tags et commentaire : écritures ciblées, comme la miniature. Un Update
// depuis une copie ancienne du job (prise avant qu'on les modifie) ne
// doit pas les écraser — ni l'inverse : SetTags/SetComment ne touchent
// qu'à leur champ.
func TestFakeStore_SetTagsAndComment_TargetedAndSurviveUpdate(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()
	s.Create(ctx, Job{ID: "a", Status: StatusRunning})
	stale, _, _ := s.Get(ctx, "a")

	if err := s.SetTags(ctx, "a", []string{"urgent"}); err != nil {
		t.Fatalf("SetTags() error = %v", err)
	}
	if err := s.SetComment(ctx, "a", "à vérifier"); err != nil {
		t.Fatalf("SetComment() error = %v", err)
	}
	stale.Status = StatusDone
	if err := s.Update(ctx, stale); err != nil {
		t.Fatal(err)
	}

	got, _, _ := s.Get(ctx, "a")
	if len(got.Tags) != 1 || got.Tags[0] != "urgent" || got.Comment != "à vérifier" {
		t.Errorf("after stale Update: tags=%v comment=%q, want both kept", got.Tags, got.Comment)
	}
	if got.Status != StatusDone {
		t.Errorf("Status = %s, want done (Update must still apply its own fields)", got.Status)
	}
}

func TestFakeStore_SetTagsAndComment_UnknownJob_ReturnsError(t *testing.T) {
	s := NewFakeStore()
	if err := s.SetTags(context.Background(), "nope", []string{"x"}); err == nil {
		t.Error("SetTags() error = nil, want an error for an unknown job")
	}
	if err := s.SetComment(context.Background(), "nope", "x"); err == nil {
		t.Error("SetComment() error = nil, want an error for an unknown job")
	}
}
