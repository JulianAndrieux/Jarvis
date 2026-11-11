package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// MongoStore : conversations, mémoire et consommation dans trois
// collections (<préfixe>conversations, <préfixe>facts, <préfixe>usage).
// Chaque filtre porte l'environnement et le user de la portée.
type MongoStore struct {
	Conversations *mongo.Collection
	FactsColl     *mongo.Collection
	Usage         *mongo.Collection
	scope         tenancy.Scope
}

func NewMongoStore(ctx context.Context, uri, database, prefix string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("assistant: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("assistant: ping mongodb: %w", err)
	}
	db := client.Database(database)
	return &MongoStore{
		Conversations: db.Collection(prefix + "conversations"),
		FactsColl:     db.Collection(prefix + "facts"),
		Usage:         db.Collection(prefix + "usage"),
		scope:         tenancy.LocalScope(),
	}, nil
}

func (s *MongoStore) For(scope tenancy.Scope) Store {
	c := *s
	c.scope = scope
	return &c
}

func (s *MongoStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("assistant: mongo: %w", err)
	}
	return nil
}

// owner : le filtre qui cloisonne (environnement + user).
func (s *MongoStore) owner() bson.M {
	return bson.M{"env_id": string(s.scope.Env), "user_id": string(s.scope.User)}
}

func (s *MongoStore) key(id string) bson.M {
	f := s.owner()
	f["_id"] = id
	return f
}

func (s *MongoStore) Create(ctx context.Context, c Conversation) error {
	if err := s.ensure(); err != nil {
		return err
	}
	c.Env, c.User = s.scope.Env, s.scope.User
	if c.Entries == nil {
		c.Entries = []Entry{}
	}
	if _, err := s.Conversations.InsertOne(ctx, c); err != nil {
		return fmt.Errorf("assistant: créer la conversation %s : %w", c.ID, err)
	}
	return nil
}

func (s *MongoStore) Get(ctx context.Context, id string) (Conversation, bool, error) {
	if err := s.ensure(); err != nil {
		return Conversation{}, false, err
	}
	var c Conversation
	err := s.Conversations.FindOne(ctx, s.key(id)).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("assistant: lire la conversation %s : %w", id, err)
	}
	return c, true, nil
}

func (s *MongoStore) List(ctx context.Context, limit int) ([]Conversation, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultListLimit
	}
	cur, err := s.Conversations.Find(ctx, s.owner(), options.Find().
		SetProjection(bson.M{"entries": 0}).
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: 1}}).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("assistant: lister les conversations : %w", err)
	}
	var out []Conversation
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("assistant: lister les conversations : %w", err)
	}
	return out, nil
}

// one applique update à la conversation id de la portée.
func (s *MongoStore) one(ctx context.Context, id string, update bson.M, what string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Conversations.UpdateOne(ctx, s.key(id), update)
	if err != nil {
		return fmt.Errorf("assistant: %s (%s) : %w", what, id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w : conversation %s", ErrNotFound, id)
	}
	return nil
}

func (s *MongoStore) Append(ctx context.Context, id string, at time.Time, entries ...Entry) error {
	update := bson.M{"$set": bson.M{"updated_at": at}}
	if len(entries) > 0 {
		update["$push"] = bson.M{"entries": bson.M{"$each": entries}}
	}
	return s.one(ctx, id, update, "ajouter au fil")
}

func (s *MongoStore) Start(ctx context.Context, id string, at time.Time) (bool, error) {
	if err := s.ensure(); err != nil {
		return false, err
	}
	// Une seule écriture conditionnelle : c'est la base qui départage deux
	// départs simultanés.
	filter := s.key(id)
	filter["status"] = bson.M{"$ne": string(StatusRunning)}
	res, err := s.Conversations.UpdateOne(ctx, filter,
		bson.M{"$set": bson.M{"status": string(StatusRunning), "error": "", "updated_at": at}})
	if err != nil {
		return false, fmt.Errorf("assistant: démarrer un tour (%s) : %w", id, err)
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	n, err := s.Conversations.CountDocuments(ctx, s.key(id))
	if err != nil {
		return false, fmt.Errorf("assistant: démarrer un tour (%s) : %w", id, err)
	}
	if n == 0 {
		return false, fmt.Errorf("%w : conversation %s", ErrNotFound, id)
	}
	return false, nil
}

func (s *MongoStore) Finish(ctx context.Context, id string, errMsg string, at time.Time) error {
	status := StatusIdle
	if errMsg != "" {
		status = StatusFailed
	}
	return s.one(ctx, id, bson.M{"$set": bson.M{"status": string(status), "error": errMsg, "updated_at": at}}, "clore le tour")
}

func (s *MongoStore) Delete(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Conversations.DeleteOne(ctx, s.key(id))
	if err != nil {
		return fmt.Errorf("assistant: supprimer la conversation %s : %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("%w : conversation %s", ErrNotFound, id)
	}
	return nil
}

// FailRunning : voir Recoverer.
func (s *MongoStore) FailRunning(ctx context.Context, errMsg string, at time.Time) (int, error) {
	res, err := s.Conversations.UpdateMany(ctx, bson.M{"status": string(StatusRunning)},
		bson.M{"$set": bson.M{"status": string(StatusFailed), "error": errMsg, "updated_at": at}})
	if err != nil {
		return 0, fmt.Errorf("assistant: reprise des tours interrompus : %w", err)
	}
	return int(res.ModifiedCount), nil
}

type factDoc struct {
	Fact `bson:",inline"`
	Env  string `bson:"env_id"`
	User string `bson:"user_id"`
}

func (s *MongoStore) AddFact(ctx context.Context, f Fact) error {
	if err := s.ensure(); err != nil {
		return err
	}
	if _, err := s.FactsColl.InsertOne(ctx, factDoc{Fact: f, Env: string(s.scope.Env), User: string(s.scope.User)}); err != nil {
		return fmt.Errorf("assistant: retenir : %w", err)
	}
	return nil
}

func (s *MongoStore) Facts(ctx context.Context) ([]Fact, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	cur, err := s.FactsColl.Find(ctx, s.owner(), options.Find().
		SetSort(bson.D{{Key: "at", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(500))
	if err != nil {
		return nil, fmt.Errorf("assistant: lire la mémoire : %w", err)
	}
	var docs []factDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("assistant: lire la mémoire : %w", err)
	}
	out := make([]Fact, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Fact)
	}
	return out, nil
}

func (s *MongoStore) DeleteFact(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.FactsColl.DeleteOne(ctx, s.key(id))
	if err != nil {
		return fmt.Errorf("assistant: oublier %s : %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("%w : fait %s", ErrNotFound, id)
	}
	return nil
}

func (s *MongoStore) usageID(day string) string { return string(s.scope.Env) + "/" + day }

func (s *MongoStore) AddUsage(ctx context.Context, day string, u claudeapi.Usage) error {
	if err := s.ensure(); err != nil {
		return err
	}
	_, err := s.Usage.UpdateOne(ctx, bson.M{"_id": s.usageID(day)}, bson.M{
		"$inc":         bson.M{"input": u.Input, "output": u.Output, "cache_read": u.CacheRead, "cache_write": u.CacheWrite},
		"$setOnInsert": bson.M{"env_id": string(s.scope.Env), "day": day},
	}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("assistant: compter la consommation : %w", err)
	}
	return nil
}

func (s *MongoStore) UsageOn(ctx context.Context, day string) (claudeapi.Usage, error) {
	if err := s.ensure(); err != nil {
		return claudeapi.Usage{}, err
	}
	var u claudeapi.Usage
	err := s.Usage.FindOne(ctx, bson.M{"_id": s.usageID(day)}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return claudeapi.Usage{}, nil
	}
	if err != nil {
		return claudeapi.Usage{}, fmt.Errorf("assistant: lire la consommation : %w", err)
	}
	return u, nil
}
