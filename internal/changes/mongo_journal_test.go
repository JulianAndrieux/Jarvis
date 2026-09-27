//go:build integration

package changes

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Le même contrat que FakeJournal, contre Atlas : cloisonnement, filtres,
// ordre.
func TestMongoJournal(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	j, err := NewMongoJournal(ctx, uri, "jarvis", "changes_test")
	if err != nil {
		t.Fatalf("NewMongoJournal : %v", err)
	}
	stamp := "j" + time.Now().Format("150405.000000")
	envA, envB := tenancy.EnvID("A-"+stamp), tenancy.EnvID("B-"+stamp)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = j.Ops.DeleteMany(c, bson.M{"env_id": bson.M{"$in": []string{string(envA), string(envB)}}})
	})

	at := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	ops := []Op{
		{ID: stamp + "-1", Env: envA, User: "u-a", Session: "s-a", At: at, Kind: KindNote, Target: "n1", Field: "title", Action: Set, After: `"Courses"`},
		{ID: stamp + "-2", Env: envA, User: "u-a", Session: "s-a", At: at.Add(time.Minute), Kind: KindDocument, Target: "d1", Field: "tags", Action: Set},
		{ID: stamp + "-3", Env: envB, User: "u-b", Session: "s-b", At: at, Kind: KindNote, Target: "n9", Action: Create},
	}
	if err := j.Append(ctx, ops...); err != nil {
		t.Fatalf("Append : %v", err)
	}

	got, err := j.List(ctx, Query{Env: envA})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d opérations pour A, veut 2 (le journal est cloisonné)", len(got))
	}
	if got[0].Target != "d1" {
		t.Errorf("la plus récente d'abord, reçu %q", got[0].Target)
	}
	if got[1].After != `"Courses"` {
		t.Errorf("After = %q : le contenu doit survivre à l'aller-retour", got[1].After)
	}
	hist, err := j.List(ctx, Query{Env: envA, Kind: KindNote, Target: "n1"})
	if err != nil || len(hist) != 1 {
		t.Errorf("historique de n1 = %d (err=%v), veut 1", len(hist), err)
	}
	mine, err := j.List(ctx, Query{Env: envA, User: "u-a"})
	if err != nil || len(mine) != 2 {
		t.Errorf("activité de u-a = %d (err=%v), veut 2", len(mine), err)
	}
	if other, _ := j.List(ctx, Query{Env: envA, User: "u-b"}); len(other) != 0 {
		t.Errorf("%d opérations pour u-b dans A, veut 0", len(other))
	}
}
