package tickets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStore persiste les tickets dans une collection MongoDB.
type MongoStore struct {
	Collection *mongo.Collection
}

func NewMongoStore(ctx context.Context, uri, database, collection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("tickets: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("tickets: ping mongodb: %w", err)
	}
	return &MongoStore{Collection: client.Database(database).Collection(collection)}, nil
}

func (s *MongoStore) Create(ctx context.Context, t Ticket) error {
	if _, err := s.Collection.InsertOne(ctx, t); err != nil {
		return fmt.Errorf("tickets: create %s: %w", t.ID, err)
	}
	return nil
}

func (s *MongoStore) Get(ctx context.Context, id string) (Ticket, bool, error) {
	var t Ticket
	err := s.Collection.FindOne(ctx, bson.M{"_id": id}).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Ticket{}, false, nil
	}
	if err != nil {
		return Ticket{}, false, fmt.Errorf("tickets: get %s: %w", id, err)
	}
	return t, true, nil
}

func (s *MongoStore) Update(ctx context.Context, t Ticket) error {
	res, err := s.Collection.UpdateByID(ctx, t.ID, bson.M{"$set": bson.M{
		"title": t.Title, "need": t.Need, "acceptance": t.Acceptance,
		"status": t.Status, "plan": t.Plan, "updated_at": time.Now(),
		"branch": t.Branch, "diff": t.Diff, "report": t.Report,
	}})
	if err != nil {
		return fmt.Errorf("tickets: update %s: %w", t.ID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("tickets: update %s: not found", t.ID)
	}
	return nil
}

func (s *MongoStore) List(ctx context.Context, status Status) ([]Ticket, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	// Le fil peut être long : pas chargé pour une liste.
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetProjection(bson.M{"events": 0, "diff": 0, "report": 0}).SetLimit(500)
	cur, err := s.Collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("tickets: list: %w", err)
	}
	var out []Ticket
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("tickets: list: %w", err)
	}
	return out, nil
}

func (s *MongoStore) AppendEvent(ctx context.Context, id string, e Event) error {
	res, err := s.Collection.UpdateByID(ctx, id, bson.M{"$push": bson.M{"events": e}, "$set": bson.M{"updated_at": time.Now()}})
	if err != nil {
		return fmt.Errorf("tickets: append event %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("tickets: append event %s: not found", id)
	}
	return nil
}

func (s *MongoStore) Delete(ctx context.Context, id string) error {
	res, err := s.Collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("tickets: delete %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("tickets: delete %s: not found", id)
	}
	return nil
}
