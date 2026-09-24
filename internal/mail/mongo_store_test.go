//go:build integration

package mail

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "emails_test", "email_files_test")
	if err != nil {
		t.Fatal(err)
	}
	stamp := "mongo" + time.Now().Format("150405.000000")
	storeContract(t, s, stamp)
	re := bson.M{"$regex": "^" + stamp}
	s.Mails.DeleteMany(ctx, bson.M{"_id": re})
	s.Files.DeleteMany(ctx, bson.M{"_id": re})
}
