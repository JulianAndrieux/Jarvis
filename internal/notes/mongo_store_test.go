//go:build integration

package notes

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Même contrat que FakeStore, contre Atlas (collections de test).
func TestMongoStore_Contract(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "notes_test", "tasks_test")
	if err != nil {
		t.Fatal(err)
	}
	storeContract(t, s, "mongo"+time.Now().Format("150405.000000"))
}

// Une recherche contenant des caractères spéciaux d'expression régulière
// est cherchée telle quelle, sans erreur.
func TestMongoStore_SearchIsLiteral(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx := context.Background()
	s, err := NewMongoStore(ctx, uri, "jarvis", "notes_test", "tasks_test")
	if err != nil {
		t.Fatal(err)
	}
	id := "lit-" + time.Now().Format("150405.000000")
	s.CreateNote(ctx, Note{ID: id, Title: "Budget (" + id + ") 50 % *", UpdatedAt: time.Now()})
	defer s.DeleteNote(ctx, id)
	got, err := s.ListNotes(ctx, NoteQuery{Search: "(" + id + ") 50 % *"})
	if err != nil || len(got) != 1 {
		t.Errorf("ListNotes = %d notes, %v", len(got), err)
	}
}

// Le contrat d'isolation contre une vraie base : c'est là qu'un filtre
// Mongo oublié se verrait. La fake le passe aussi
// (TestFakeStore_IsolationContract) — les deux doivent se comporter
// pareil, leçon du jalon 23.
func TestMongoStore_IsolationContract(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "notes_test", "tasks_test")
	if err != nil {
		t.Fatal(err)
	}
	stamp := "iso" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		c := context.Background()
		for _, env := range []string{"A-" + stamp, "B-" + stamp} {
			_, _ = s.Notes.DeleteMany(c, bson.M{"env_id": env})
			_, _ = s.Tasks.DeleteMany(c, bson.M{"env_id": env})
		}
	})
	storeIsolationContract(t, s, stamp)
}
