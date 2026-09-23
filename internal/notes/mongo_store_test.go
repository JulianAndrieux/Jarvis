//go:build integration

package notes

import (
	"context"
	"os"
	"testing"
	"time"
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
