package agents

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStore persiste les prompts des agents, un document par agent.
type MongoStore struct {
	Collection *mongo.Collection
}

func NewMongoStore(ctx context.Context, uri, database, collection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("agents: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("agents: ping mongodb: %w", err)
	}
	return &MongoStore{Collection: client.Database(database).Collection(collection)}, nil
}

type mongoVersion struct {
	Prompt string    `bson:"prompt"`
	At     time.Time `bson:"at"`
}

type mongoRecord struct {
	ID        string         `bson:"_id"`
	Prompt    string         `bson:"prompt"`
	UpdatedAt time.Time      `bson:"updated_at"`
	History   []mongoVersion `bson:"history"`
}

func (s *MongoStore) List(ctx context.Context) ([]Record, error) {
	cur, err := s.Collection.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("agents: list: %w", err)
	}
	defer cur.Close(ctx)
	var out []Record
	for cur.Next(ctx) {
		var doc mongoRecord
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("agents: decode: %w", err)
		}
		r := Record{ID: doc.ID, Prompt: doc.Prompt, UpdatedAt: doc.UpdatedAt}
		for _, v := range doc.History {
			r.History = append(r.History, Version{Prompt: v.Prompt, At: v.At})
		}
		out = append(out, r)
	}
	return out, cur.Err()
}

// Save remplace le document de l'agent (créé au premier enregistrement).
func (s *MongoStore) Save(ctx context.Context, r Record) error {
	doc := mongoRecord{ID: r.ID, Prompt: r.Prompt, UpdatedAt: r.UpdatedAt, History: []mongoVersion{}}
	for _, v := range r.History {
		doc.History = append(doc.History, mongoVersion{Prompt: v.Prompt, At: v.At})
	}
	if _, err := s.Collection.ReplaceOne(ctx, bson.M{"_id": r.ID}, doc, options.Replace().SetUpsert(true)); err != nil {
		return fmt.Errorf("agents: save %s: %w", r.ID, err)
	}
	return nil
}
