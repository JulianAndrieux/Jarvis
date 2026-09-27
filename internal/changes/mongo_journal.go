package changes

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoJournal persiste le journal dans une collection. Append-only : ni
// Update ni Delete — une piste d'audit qu'on peut réécrire n'en est pas
// une.
type MongoJournal struct {
	Ops *mongo.Collection
}

func NewMongoJournal(ctx context.Context, uri, database, collection string) (*MongoJournal, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("changes: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("changes: ping mongodb: %w", err)
	}
	j := &MongoJournal{Ops: client.Database(database).Collection(collection)}
	// Les deux lectures du journal : l'activité d'un environnement (par
	// date) et l'historique d'une entité.
	if _, err := j.Ops.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "env_id", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "env_id", Value: 1}, {Key: "kind", Value: 1}, {Key: "target", Value: 1}, {Key: "at", Value: -1}}},
	}); err != nil {
		return nil, fmt.Errorf("changes: index: %w", err)
	}
	return j, nil
}

func (j *MongoJournal) Append(ctx context.Context, ops ...Op) error {
	if len(ops) == 0 {
		return nil
	}
	docs := make([]any, 0, len(ops))
	for _, o := range ops {
		docs = append(docs, o)
	}
	if _, err := j.Ops.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("changes: append: %w", err)
	}
	return nil
}

func (j *MongoJournal) List(ctx context.Context, q Query) ([]Op, error) {
	limit := int64(q.Limit)
	if limit == 0 {
		limit = int64(DefaultLimit)
	}
	filter := bson.M{"env_id": string(q.Env)}
	if q.User != "" {
		filter["user_id"] = string(q.User)
	}
	if q.Session != "" {
		filter["session_id"] = string(q.Session)
	}
	if q.Kind != "" {
		filter["kind"] = string(q.Kind)
	}
	if q.Target != "" {
		filter["target"] = q.Target
	}
	cur, err := j.Ops.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("changes: list: %w", err)
	}
	var out []Op
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("changes: list: %w", err)
	}
	return out, nil
}
