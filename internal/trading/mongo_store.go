package trading

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// MongoStore : la liste de suivi dans une collection MongoDB. Identifiant
// d'un document : environnement + slug — un même marché suivi par deux
// environnements fait deux documents, et l'unicité est garantie par la
// base elle-même (pas de lecture puis écriture).
type MongoStore struct {
	Watches *mongo.Collection
	scope   tenancy.Scope
}

func NewMongoStore(ctx context.Context, uri, database, watchesCollection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("trading: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("trading: ping mongodb: %w", err)
	}
	return &MongoStore{Watches: client.Database(database).Collection(watchesCollection), scope: tenancy.System(tenancy.Local)}, nil
}

func (s *MongoStore) For(scope tenancy.Scope) Store {
	c := *s
	c.scope = scope
	return &c
}

func (s *MongoStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("trading: mongo: %w", err)
	}
	return nil
}

func (s *MongoStore) id(slug string) string { return string(s.scope.Env) + "/" + slug }

func (s *MongoStore) key(slug string) bson.M {
	return bson.M{"_id": s.id(slug), "env_id": string(s.scope.Env)}
}

type watchDoc struct {
	ID    string `bson:"_id"`
	Watch `bson:",inline"`
}

func (s *MongoStore) AddWatch(ctx context.Context, w Watch) error {
	if err := s.ensure(); err != nil {
		return err
	}
	w.Env = s.scope.Env
	_, err := s.Watches.InsertOne(ctx, watchDoc{ID: s.id(w.Slug), Watch: w})
	if mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("%w : %s", ErrAlreadyWatched, w.Slug)
	}
	if err != nil {
		return fmt.Errorf("trading: suivre %s : %w", w.Slug, err)
	}
	return nil
}

func (s *MongoStore) GetWatch(ctx context.Context, slug string) (Watch, bool, error) {
	if err := s.ensure(); err != nil {
		return Watch{}, false, err
	}
	var d watchDoc
	err := s.Watches.FindOne(ctx, s.key(slug)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Watch{}, false, nil
	}
	if err != nil {
		return Watch{}, false, fmt.Errorf("trading: lire %s : %w", slug, err)
	}
	return d.Watch, true, nil
}

func (s *MongoStore) ListWatches(ctx context.Context) ([]Watch, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	cur, err := s.Watches.Find(ctx, bson.M{"env_id": string(s.scope.Env)},
		options.Find().SetSort(bson.D{{Key: "added_at", Value: -1}, {Key: "slug", Value: 1}}).SetLimit(500))
	if err != nil {
		return nil, fmt.Errorf("trading: lister : %w", err)
	}
	var docs []watchDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("trading: lister : %w", err)
	}
	out := make([]Watch, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Watch)
	}
	return out, nil
}

func (s *MongoStore) SetWatchNote(ctx context.Context, slug, note string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Watches.UpdateOne(ctx, s.key(slug), bson.M{"$set": bson.M{"note": note}})
	if err != nil {
		return fmt.Errorf("trading: note de %s : %w", slug, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w : %s", ErrNotFound, slug)
	}
	return nil
}

func (s *MongoStore) RemoveWatch(ctx context.Context, slug string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Watches.DeleteOne(ctx, s.key(slug))
	if err != nil {
		return fmt.Errorf("trading: retirer %s : %w", slug, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("%w : %s", ErrNotFound, slug)
	}
	return nil
}
