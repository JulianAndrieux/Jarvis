package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// MongoStore : les conversations dans une collection MongoDB, un document
// par conversation. Chaque filtre porte l'environnement et le user : la
// conversation d'un autre ne correspond à aucune requête.
type MongoStore struct {
	Conversations *mongo.Collection
	scope         tenancy.Scope
}

func NewMongoStore(ctx context.Context, uri, database, collection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("assistant: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("assistant: ping mongodb: %w", err)
	}
	return &MongoStore{Conversations: client.Database(database).Collection(collection), scope: tenancy.LocalScope()}, nil
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

func (s *MongoStore) owner() bson.M {
	return bson.M{"env_id": string(s.scope.Env), "user_id": string(s.scope.User)}
}

func (s *MongoStore) key(id string) bson.M {
	k := s.owner()
	k["_id"] = id
	return k
}

func (s *MongoStore) Create(ctx context.Context, c Conversation) error {
	if err := s.ensure(); err != nil {
		return err
	}
	c.Env, c.User = s.scope.Env, s.scope.User
	if _, err := s.Conversations.InsertOne(ctx, c); err != nil {
		return fmt.Errorf("assistant: créer %s : %w", c.ID, err)
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
		return Conversation{}, false, fmt.Errorf("assistant: lire %s : %w", id, err)
	}
	return c, true, nil
}

func (s *MongoStore) List(ctx context.Context) ([]Conversation, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	cur, err := s.Conversations.Find(ctx, s.owner(), options.Find().
		SetProjection(bson.M{"messages": 0, "live": 0}).
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: 1}}).
		SetLimit(MaxList))
	if err != nil {
		return nil, fmt.Errorf("assistant: lister : %w", err)
	}
	var out []Conversation
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("assistant: lister : %w", err)
	}
	return out, nil
}

// missing distingue « introuvable » de « occupée » quand une écriture
// conditionnelle n'a rien touché.
func (s *MongoStore) missing(ctx context.Context, id string, otherwise error) error {
	if _, ok, err := s.Get(ctx, id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	return otherwise
}

func (s *MongoStore) Begin(ctx context.Context, id string, user Message, staleBefore time.Time) error {
	if err := s.ensure(); err != nil {
		return err
	}
	filter := s.key(id)
	filter["$or"] = bson.A{
		bson.M{"running": bson.M{"$ne": true}},
		bson.M{"run_started_at": bson.M{"$lt": staleBefore}},
	}
	res, err := s.Conversations.UpdateOne(ctx, filter, bson.M{
		"$push": bson.M{"messages": user},
		"$set":  bson.M{"running": true, "run_started_at": user.At, "live": bson.A{}, "updated_at": user.At},
	})
	if err != nil {
		return fmt.Errorf("assistant: question dans %s : %w", id, err)
	}
	if res.MatchedCount == 0 {
		return s.missing(ctx, id, ErrBusy)
	}
	return nil
}

func (s *MongoStore) AddStep(ctx context.Context, id string, step Step) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Conversations.UpdateOne(ctx, s.key(id), bson.M{"$push": bson.M{"live": step}})
	if err != nil {
		return fmt.Errorf("assistant: étape dans %s : %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	return nil
}

func (s *MongoStore) Finish(ctx context.Context, id string, reply Message) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Conversations.UpdateOne(ctx, s.key(id), bson.M{
		"$push": bson.M{"messages": reply},
		"$set":  bson.M{"running": false, "run_started_at": time.Time{}, "live": bson.A{}, "updated_at": reply.At},
	})
	if err != nil {
		return fmt.Errorf("assistant: réponse dans %s : %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	return nil
}

func (s *MongoStore) Delete(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	res, err := s.Conversations.DeleteOne(ctx, s.key(id))
	if err != nil {
		return fmt.Errorf("assistant: supprimer %s : %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	return nil
}
