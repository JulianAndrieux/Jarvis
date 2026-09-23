//go:build integration

package webapp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// newTestMongoStore se connecte à une vraie base (MONGO_URI doit être
// exporté — voir CLAUDE.md, "Application web unifiée"). Sans elle, le
// test est ignoré plutôt qu'en échec : -tags=integration seul ne suffit
// pas à garantir un accès Atlas (contrairement à pdftotext/pdftoppm,
// MONGO_URI est un identifiant, pas un outil installable).
func newTestMongoStore(t *testing.T) *MongoStore {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré (voir CLAUDE.md)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := NewMongoStore(ctx, uri, "jarvis", "jobs_test")
	if err != nil {
		t.Fatalf("NewMongoStore() error = %v", err)
	}
	return store
}

func cleanupJob(t *testing.T, store *MongoStore, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Delete retire aussi les fichiers GridFS du job (jalon 25) ; repli
	// sur la suppression brute du document si le job est déjà absent.
	if err := store.Delete(ctx, id); err != nil {
		if _, err := store.Collection.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
			t.Logf("cleanup: delete %s: %v", id, err)
		}
	}
}

// TestMongoStore_Update_PersistsDocTypeDeterminedAfterCreate est un test
// de non-régression pour un bug réel trouvé en testant un vrai upload
// bout en bout : DocType n'est connu qu'à la fin du traitement
// (classification automatique, jalon 15), mais Update() ne l'écrivait
// pas dans son $set — Get() après Update() renvoyait donc toujours
// DocType == "" malgré une classification réussie.
func TestMongoStore_Update_PersistsDocTypeDeterminedAfterCreate(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()

	job := Job{
		ID:       "test-doctype-" + time.Now().Format("20060102150405"),
		Filename: "doc.pdf", Content: []byte("%PDF-1.4"),
		Status: StatusPending, CreatedAt: time.Now(),
	}
	defer cleanupJob(t, store, job.ID)

	created, err := store.Create(ctx, job)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.DocType != "" {
		t.Fatalf("created.DocType = %q, want empty (pas encore classifié)", created.DocType)
	}

	created.Status = StatusDone
	created.DocType = "facture"
	created.Result = &pipeline.Result{DocType: "facture"}
	created.FinishedAt = time.Now()
	if err := store.Update(ctx, created); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, ok, err := store.Get(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	if got.DocType != "facture" {
		t.Errorf("Get().DocType = %q, want %q (déterminé après Create, doit survivre à Update+Get)", got.DocType, "facture")
	}
}

func TestMongoStore_CreateThenGet_RoundTrips(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()

	job := Job{
		ID:       "test-roundtrip-" + time.Now().Format("20060102150405"),
		Filename: "doc.pdf", Content: []byte("%PDF-1.4 contenu"),
		Status: StatusPending, CreatedAt: time.Now(),
	}
	defer cleanupJob(t, store, job.ID)

	if _, err := store.Create(ctx, job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, ok, err := store.Get(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	// Jalon 25 : le fichier vit dans GridFS, Get ne le charge plus.
	if got.Filename != "doc.pdf" || got.Content != nil {
		t.Errorf("Get() = %+v, want Filename=doc.pdf and no Content", got)
	}
	if data, ok, err := store.ReadFile(context.Background(), job.ID, FileOriginal); err != nil || !ok || string(data) != "%PDF-1.4 contenu" {
		t.Errorf("ReadFile(original) = %q ok=%v err=%v, want the uploaded content", data, ok, err)
	}
}

func TestMongoStore_Get_UnknownID_ReturnsFalse(t *testing.T) {
	store := newTestMongoStore(t)

	_, ok, err := store.Get(context.Background(), "does-not-exist-in-atlas")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if ok {
		t.Error("Get() ok = true, want false for an unknown id")
	}
}

func TestMongoStore_Update_PersistsTags(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()

	job := Job{
		ID:       "test-tags-" + time.Now().Format("20060102150405"),
		Filename: "doc.pdf", Status: StatusPending, CreatedAt: time.Now(),
	}
	defer cleanupJob(t, store, job.ID)

	created, err := store.Create(ctx, job)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	created.Tags = []string{"urgent", "a-revoir"}
	if err := store.Update(ctx, created); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, ok, err := store.Get(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "urgent" || got.Tags[1] != "a-revoir" {
		t.Errorf("Get().Tags = %v, want [urgent a-revoir]", got.Tags)
	}
}

func TestMongoStore_List_FiltersBySearchAndSortsByDateDescending(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	base := time.Now()
	suffix := time.Now().Format("20060102150405.000000")

	older := Job{ID: "test-list-older-" + suffix, Filename: "ancien-facture.pdf", DocType: "facture", CreatedAt: base}
	newer := Job{ID: "test-list-newer-" + suffix, Filename: "recent-facture.pdf", DocType: "facture", CreatedAt: base.Add(time.Hour)}
	other := Job{ID: "test-list-other-" + suffix, Filename: "sans-rapport.pdf", DocType: "piece_identite", CreatedAt: base.Add(2 * time.Hour)}
	for _, j := range []Job{older, newer, other} {
		if _, err := store.Create(ctx, j); err != nil {
			t.Fatal(err)
		}
		defer cleanupJob(t, store, j.ID)
	}

	// Filtré ensuite aux seuls ID de ce test pour ignorer les données
	// réelles éventuellement présentes dans jobs_test.
	got, err := store.List(ctx, ListQuery{Search: "facture"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var ids []string
	for _, j := range got {
		if j.ID == older.ID || j.ID == newer.ID || j.ID == other.ID {
			ids = append(ids, j.ID)
		}
	}
	if len(ids) != 2 || ids[0] != newer.ID || ids[1] != older.ID {
		t.Errorf("filtered+ordered ids = %v, want [%s %s] (recent facture, ancien facture — other excluded, newest first)", ids, newer.ID, older.ID)
	}
}

func TestMongoStore_List_FiltersBySearchText(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-searchtext-" + time.Now().Format("20060102150405.000000")

	job := Job{ID: id, Filename: "sans-mot-cle.pdf", CreatedAt: time.Now(), SearchText: "Contient le mot Kangourou dans le texte du document"}
	if _, err := store.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	defer cleanupJob(t, store, id)

	got, err := store.List(ctx, ListQuery{Search: "kangourou"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	found := false
	for _, j := range got {
		if j.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("List(Search=kangourou) did not find the job whose SearchText contains it")
	}
}

func TestMongoStore_List_FiltersByExactStatus(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	suffix := time.Now().Format("20060102150405.000000")

	running := Job{ID: "test-status-running-" + suffix, Filename: "en-cours.pdf", Status: StatusRunning, CreatedAt: time.Now()}
	done := Job{ID: "test-status-done-" + suffix, Filename: "termine.pdf", Status: StatusDone, CreatedAt: time.Now()}
	for _, j := range []Job{running, done} {
		if _, err := store.Create(ctx, j); err != nil {
			t.Fatal(err)
		}
		defer cleanupJob(t, store, j.ID)
	}

	// Filtré ensuite aux seuls ID de ce test pour ignorer les données
	// réelles éventuellement déjà "running"/"done" dans jobs_test.
	got, err := store.List(ctx, ListQuery{Status: StatusRunning})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	foundRunning, foundDone := false, false
	for _, j := range got {
		if j.ID == running.ID {
			foundRunning = true
		}
		if j.ID == done.ID {
			foundDone = true
		}
	}
	if !foundRunning {
		t.Errorf("List(Status=running) did not find the running job")
	}
	if foundDone {
		t.Errorf("List(Status=running) found the done job, want it excluded")
	}
}

func TestMongoStore_Delete_RemovesJob(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-delete-" + time.Now().Format("20060102150405.000000")

	if _, err := store.Create(ctx, Job{ID: id, Filename: "a-supprimer.pdf", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}

	_, ok, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Get() ok = true after Delete(), want false")
	}
}

func TestMongoStore_Delete_UnknownID_ReturnsError(t *testing.T) {
	store := newTestMongoStore(t)

	err := store.Delete(context.Background(), "does-not-exist-in-atlas")
	if err == nil {
		t.Fatal("Delete() error = nil, want non-nil for an unknown job id")
	}
}

// Jalon 22 : la miniature est écrite par un $set ciblé (SetThumbnail),
// jamais par Update — Update reçoit un Job complet qui, chargé via une
// liste SummaryOnly, n'aurait pas de miniature et l'effacerait.
func TestMongoStore_SetThumbnail_RoundTripsAndSurvivesUpdate(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-thumb-" + time.Now().Format("150405.000000")
	defer cleanupJob(t, store, id)

	job := Job{ID: id, Filename: "t.pdf", Content: []byte("%PDF"), Status: StatusDone, CreatedAt: time.Now()}
	if _, err := store.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := store.SetThumbnail(ctx, id, []byte("png-bytes")); err != nil {
		t.Fatalf("SetThumbnail() error = %v", err)
	}
	job.Tags = []string{"x"}
	if err := store.Update(ctx, job); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get() = ok %v, err %v", ok, err)
	}
	if string(got.Thumbnail) != "png-bytes" {
		t.Errorf("Thumbnail = %q, want png-bytes (must survive Update)", got.Thumbnail)
	}
}

func TestMongoStore_SetThumbnail_UnknownID_ReturnsError(t *testing.T) {
	store := newTestMongoStore(t)
	if err := store.SetThumbnail(context.Background(), "does-not-exist-thumb", []byte("x")); err == nil {
		t.Error("SetThumbnail() error = nil, want an error for an unknown id")
	}
}

func TestMongoStore_List_SummaryOnly_OmitsHeavyFields(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-summary-" + time.Now().Format("150405.000000")
	defer cleanupJob(t, store, id)

	job := Job{ID: id, Filename: id + ".pdf", Content: []byte("%PDF"), Thumbnail: []byte("png"), Status: StatusDone, DocType: "facture", CreatedAt: time.Now(), Result: &pipeline.Result{DocType: "facture"}}
	if _, err := store.Create(ctx, job); err != nil {
		t.Fatal(err)
	}

	got, err := store.List(ctx, ListQuery{Search: id, SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("List() = %d jobs, want 1", len(got))
	}
	if got[0].Content != nil || got[0].Thumbnail != nil || got[0].Result != nil {
		t.Errorf("List(SummaryOnly) kept heavy fields: content=%d thumb=%d result=%v", len(got[0].Content), len(got[0].Thumbnail), got[0].Result)
	}
	if got[0].DocType != "facture" || got[0].Filename != id+".pdf" {
		t.Errorf("List(SummaryOnly) = %+v, want summary fields kept", got[0])
	}
}

func TestMongoStore_SetProgress_RoundTripsSurvivesUpdateAndClears(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-progress-" + time.Now().Format("150405.000000")
	defer cleanupJob(t, store, id)

	job := Job{ID: id, Filename: "p.pdf", Content: []byte("%PDF"), Status: StatusRunning, CreatedAt: time.Now()}
	if _, err := store.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Progress{Stage: pipeline.StageParsing, PageCount: 5, ParseTotal: 5, ParseDone: 2,
		Pages: []pipeline.PageContent{{Page: 1, Text: "texte page 1", Source: pipeline.SourceVLM}}}
	if err := store.SetProgress(ctx, id, p); err != nil {
		t.Fatalf("SetProgress() error = %v", err)
	}
	job.Status = StatusFailed
	if err := store.Update(ctx, job); err != nil {
		t.Fatal(err)
	}

	got, _, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress == nil || got.Progress.ParseDone != 2 || len(got.Progress.Pages) != 1 || got.Progress.Pages[0].Text != "texte page 1" {
		t.Errorf("Progress = %+v, want the stored progress (must survive Update)", got.Progress)
	}

	if err := store.SetProgress(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, id)
	if got.Progress != nil {
		t.Errorf("Progress = %+v, want nil after clearing", got.Progress)
	}
}

// --- Jalon 25 : fichiers dans GridFS (plus de limite de 16 Mo) ---

func TestMongoStore_LargeFileGoesToGridFSAndGetStaysLight(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-gridfs-" + time.Now().Format("150405.000000")
	defer cleanupJob(t, store, id)

	big := bytes.Repeat([]byte("0123456789abcdef"), 17<<20/16) // 17 Mio : au-delà de la limite d'un document MongoDB
	job := Job{ID: id, Filename: "gros.pptx", Content: big, Status: StatusPending, CreatedAt: time.Now(),
		Format: "slides", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Size: int64(len(big)), SourceHash: "abc"}
	if _, err := store.Create(ctx, job); err != nil {
		t.Fatalf("Create(17 MiB) error = %v, want success (GridFS)", err)
	}

	got, ok, err := store.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if got.Content != nil {
		t.Errorf("Get() returned %d bytes of content, want none (loaded on demand only)", len(got.Content))
	}
	if got.Format != "slides" || got.Size != int64(len(big)) || got.MIME == "" || got.SourceHash != "abc" {
		t.Errorf("Get() metadata = format %q size %d mime %q hash %q", got.Format, got.Size, got.MIME, got.SourceHash)
	}
	data, ok, err := store.ReadFile(ctx, id, FileOriginal)
	if err != nil || !ok || !bytes.Equal(data, big) {
		t.Errorf("ReadFile(original) = %d bytes ok=%v err=%v, want the 17 MiB back intact", len(data), ok, err)
	}
}

func TestMongoStore_WriteFileOverwritesAndDeleteRemovesFiles(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-files-" + time.Now().Format("150405.000000")

	if _, err := store.Create(ctx, Job{ID: id, Filename: "a.docx", Content: []byte("docx"), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ReadFile(ctx, id, FileRendition); ok {
		t.Error("ReadFile(rendition) ok = true before any WriteFile")
	}
	for _, v := range []string{"%PDF-v1", "%PDF-v2"} {
		if err := store.WriteFile(ctx, id, FileRendition, []byte(v)); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", v, err)
		}
	}
	if data, _, _ := store.ReadFile(ctx, id, FileRendition); string(data) != "%PDF-v2" {
		t.Errorf("ReadFile(rendition) = %q, want the last write", data)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []FileName{FileOriginal, FileRendition} {
		if _, ok, err := store.ReadFile(ctx, id, name); ok || err != nil {
			t.Errorf("ReadFile(%s) after Delete: ok=%v err=%v, want not found", name, ok, err)
		}
	}
}

// Les documents créés avant le jalon 25 ont leur PDF dans le champ
// "content" du document : toujours lisibles, sans migration.
func TestMongoStore_ReadFile_LegacyInlineContent(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-legacy-" + time.Now().Format("150405.000000")
	defer cleanupJob(t, store, id)

	if _, err := store.Collection.InsertOne(ctx, bson.M{"_id": id, "filename": "ancien.pdf", "content": []byte("%PDF-ancien"), "status": "done", "created_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(ctx, id)
	if err != nil || !ok || got.Content != nil {
		t.Fatalf("Get(legacy) ok=%v err=%v content=%d, want found without content", ok, err, len(got.Content))
	}
	data, ok, err := store.ReadFile(ctx, id, FileOriginal)
	if err != nil || !ok || string(data) != "%PDF-ancien" {
		t.Errorf("ReadFile(original) on a legacy job = %q ok=%v err=%v, want the inline content", data, ok, err)
	}
}

func TestMongoStore_List_FiltersByFormatIncludingLegacyPDFs(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	stamp := time.Now().Format("150405.000000")
	for _, j := range []Job{
		{ID: "fmt-sheet-" + stamp, Filename: "fmt-" + stamp + ".xlsx", Format: "sheet", CreatedAt: time.Now()},
		{ID: "fmt-pdf-" + stamp, Filename: "fmt-" + stamp + ".pdf", Format: "pdf", CreatedAt: time.Now()},
		{ID: "fmt-legacy-" + stamp, Filename: "fmt-" + stamp + "-old.pdf", CreatedAt: time.Now()},
	} {
		if _, err := store.Create(ctx, j); err != nil {
			t.Fatal(err)
		}
		defer cleanupJob(t, store, j.ID)
	}
	pdfs, err := store.List(ctx, ListQuery{Search: "fmt-" + stamp, Format: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pdfs) != 2 {
		t.Errorf("List(Format=pdf) = %d, want 2 (legacy job without format counts as PDF)", len(pdfs))
	}
	sheets, _ := store.List(ctx, ListQuery{Search: "fmt-" + stamp, Format: "sheet"})
	if len(sheets) != 1 {
		t.Errorf("List(Format=sheet) = %d, want 1", len(sheets))
	}
}

// Ticket "Ajouter commentaire sur document" : le commentaire est
// persisté, et ses mots sont trouvés par la recherche.
func TestMongoStore_Comment_PersistsAndIsSearchable(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	stamp := time.Now().Format("20060102150405")
	job := Job{ID: "test-comment-" + stamp, Filename: "doc.pdf", Status: StatusPending, CreatedAt: time.Now()}
	defer cleanupJob(t, store, job.ID)

	created, err := store.Create(ctx, job)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	created.Comment = "Envoyer au comptable " + stamp
	if err := store.Update(ctx, created); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, ok, err := store.Get(ctx, job.ID)
	if err != nil || !ok || got.Comment != created.Comment {
		t.Fatalf("Get() = %q, %v, %v, want the comment back", got.Comment, ok, err)
	}
	found, err := store.List(ctx, ListQuery{Search: "COMPTABLE " + stamp})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != job.ID {
		t.Errorf("search by comment = %d jobs, want just %s", len(found), job.ID)
	}
}

// Critère d'acceptation : chaque document existant reçoit un commentaire
// vide. La migration ne touche pas un commentaire déjà écrit, et la
// relancer ne change plus rien.
func TestMongoStore_MigrateComments_AddsEmptyCommentOnlyWhereMissing(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	stamp := time.Now().Format("20060102150405")
	legacy, commented := "test-legacy-"+stamp, "test-commented-"+stamp
	defer cleanupJob(t, store, legacy)
	defer cleanupJob(t, store, commented)
	// Document d'avant le ticket : pas de champ comment du tout.
	if _, err := store.Collection.InsertOne(ctx, bson.M{"_id": legacy, "filename": "old.pdf", "status": "done", "created_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Collection.InsertOne(ctx, bson.M{"_id": commented, "filename": "c.pdf", "status": "done", "created_at": time.Now(), "comment": "garder"}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.MigrateComments(ctx); err != nil {
		t.Fatalf("MigrateComments() error = %v", err)
	}
	var doc bson.M
	if err := store.Collection.FindOne(ctx, bson.M{"_id": legacy}).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if c, ok := doc["comment"]; !ok || c != "" {
		t.Errorf("legacy comment = %v (present %v), want an empty comment", c, ok)
	}
	if got, _, _ := store.Get(ctx, commented); got.Comment != "garder" {
		t.Errorf("existing comment = %q, want it untouched", got.Comment)
	}
	n, err := store.MigrateComments(ctx)
	if err != nil || n != 0 {
		t.Errorf("second MigrateComments() = %d, %v, want 0 documents touched", n, err)
	}
}

// Filtre par date d'import (ticket "Ajouter un filtre sur les
// documents"), intervalle semi-ouvert, combiné à la recherche.
func TestMongoStore_List_FiltersByCreationDate(t *testing.T) {
	store := newTestMongoStore(t)
	ctx := context.Background()
	stamp := time.Now().Format("20060102150405")
	day := func(d int) time.Time { return time.Date(2031, 1, d, 12, 0, 0, 0, time.Local) }
	for _, d := range []int{9, 10, 12, 13} {
		job := Job{ID: fmt.Sprintf("test-date-%s-%d", stamp, d), Filename: "date-" + stamp + ".pdf", Status: StatusDone, CreatedAt: day(d)}
		defer cleanupJob(t, store, job.ID)
		if _, err := store.Create(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.List(ctx, ListQuery{Search: "date-" + stamp, CreatedFrom: day(10).Add(-12 * time.Hour), CreatedBefore: day(13).Add(-12 * time.Hour), SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasSuffix(got[0].ID, "-12") || !strings.HasSuffix(got[1].ID, "-10") {
		var ids []string
		for _, j := range got {
			ids = append(ids, j.ID)
		}
		t.Errorf("jobs = %v, want the 12th then the 10th", ids)
	}
}
