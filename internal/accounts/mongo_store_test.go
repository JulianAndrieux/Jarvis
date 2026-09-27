//go:build integration

package accounts

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Le même contrat que FakeStore, contre Atlas (collections de test).
func TestMongoStore_Contract(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré (voir CLAUDE.md)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "test_accounts_")
	if err != nil {
		t.Fatalf("NewMongoStore : %v", err)
	}
	stamp := "c" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		c := context.Background()
		re := bson.M{"$regex": "^" + stamp}
		_, _ = s.Users.DeleteMany(c, bson.M{"_id": re})
		_, _ = s.Envs.DeleteMany(c, bson.M{"_id": re})
		_, _ = s.Members.DeleteMany(c, bson.M{"env_id": re})
		_, _ = s.Sessions.DeleteMany(c, bson.M{"_id": re})
	})
	storeContract(t, s, stamp)
}
