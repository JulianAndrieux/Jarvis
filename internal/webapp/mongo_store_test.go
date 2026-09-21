//go:build integration

package webapp

import (
	"context"
	"os"
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
	if _, err := store.Collection.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		t.Logf("cleanup: delete %s: %v", id, err)
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
	if got.Filename != "doc.pdf" || string(got.Content) != "%PDF-1.4 contenu" {
		t.Errorf("Get() = %+v, want Filename=doc.pdf Content=%%PDF-1.4 contenu", got)
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
