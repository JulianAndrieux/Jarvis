//go:build integration

package collect

import (
	"context"
	"os"
	"testing"
	"time"
)

// Même contrat que FakeStore, contre Atlas (collections « market_test_* »).
func TestMongoStore_Contract(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI non défini, test ignoré")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := NewMongoStore(ctx, uri, "jarvis", "market_test_")
	if err != nil {
		t.Fatal(err)
	}
	storeContract(t, s, time.Now().Format("150405000000"))
}
