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
