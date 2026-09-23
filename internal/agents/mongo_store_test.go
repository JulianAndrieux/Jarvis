//go:build integration

package agents

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func newTestMongoStore(t *testing.T) *MongoStore {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "agents_test")
	if err != nil {
		t.Fatalf("NewMongoStore() error = %v", err)
	}
	return s
}

// Un prompt enregistré (historique compris) est relu tel quel, et un
// second enregistrement remplace le premier.
func TestMongoStore_SaveThenList(t *testing.T) {
	s := newTestMongoStore(t)
	ctx := context.Background()
	id := "test-agent-" + time.Now().Format("150405.000000")
	defer s.Collection.DeleteOne(ctx, bson.M{"_id": id})

	at := time.Now().Truncate(time.Millisecond)
	first := Record{ID: id, Prompt: "v1", UpdatedAt: at}
	second := Record{ID: id, Prompt: "v2 {{types}}", UpdatedAt: at.Add(time.Second), History: []Version{{Prompt: "v1", At: at}}}
	for _, r := range []Record{first, second} {
		if err := s.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []Record
	for _, r := range recs {
		if r.ID == id {
			got = append(got, r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("records for %s = %d, want 1", id, len(got))
	}
	g := got[0]
	if g.Prompt != "v2 {{types}}" || !g.UpdatedAt.Equal(second.UpdatedAt) || len(g.History) != 1 || g.History[0].Prompt != "v1" || !g.History[0].At.Equal(at) {
		t.Errorf("record = %+v", g)
	}
}
