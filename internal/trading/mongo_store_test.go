//go:build integration

package trading

import (
	"context"
	"os"
	"testing"
	"time"
)

func mongoStore(t *testing.T) *MongoStore {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "market_watches_test")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Même contrat que FakeStore, contre Atlas.
func TestMongoStore_Contract(t *testing.T) {
	storeContract(t, mongoStore(t), time.Now().Format("150405.000000"))
}

func TestMongoStore_Isolation(t *testing.T) {
	storeIsolationContract(t, mongoStore(t), time.Now().Format("150405.000000"))
}
