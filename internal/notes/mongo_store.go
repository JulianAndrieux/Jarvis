package notes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// MongoStore persiste notes et tâches dans deux collections MongoDB.
type MongoStore struct {
	Notes *mongo.Collection
	Tasks *mongo.Collection
	// scope : l'environnement vu par cette instance (voir For). Vide,
	// toute opération est refusée.
	scope tenancy.Scope
}

func (s *MongoStore) For(scope tenancy.Scope) Store {
	c := *s
	c.scope = scope
	return &c
}

// key : le filtre d'un document précis de cet environnement. Un
// identifiant d'un autre environnement ne correspond à rien.
func (s *MongoStore) key(id string) bson.M {
	return bson.M{"_id": id, "env_id": string(s.scope.Env)}
}

func (s *MongoStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("notes: mongo: %w", err)
	}
	return nil
}

func NewMongoStore(ctx context.Context, uri, database, notesCollection, tasksCollection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("notes: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("notes: ping mongodb: %w", err)
	}
	db := client.Database(database)
	return &MongoStore{
		Notes: db.Collection(notesCollection),
		Tasks: db.Collection(tasksCollection),
		// Scopé sur l'unique environnement d'aujourd'hui : choix du
		// câblage, que le jalon 45 remplacera par la portée de la session.
		scope: tenancy.Scope{Env: tenancy.Local, Role: tenancy.RoleOwner},
	}, nil
}

func (s *MongoStore) CreateNote(ctx context.Context, n Note) error {
	if err := s.ensure(); err != nil {
		return err
	}
	n.Env = s.scope.Env
	n.Version = 1
	if _, err := s.Notes.InsertOne(ctx, n); err != nil {
		return fmt.Errorf("notes: create note %s: %w", n.ID, err)
	}
	return nil
}

func (s *MongoStore) GetNote(ctx context.Context, id string) (Note, bool, error) {
	if err := s.ensure(); err != nil {
		return Note{}, false, err
	}
	var n Note
	err := s.Notes.FindOne(ctx, s.key(id)).Decode(&n)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Note{}, false, nil
	}
	if err != nil {
		return Note{}, false, fmt.Errorf("notes: get note %s: %w", id, err)
	}
	return n, true, nil
}

func (s *MongoStore) UpdateNote(ctx context.Context, n Note) error {
	if err := s.ensure(); err != nil {
		return err
	}
	// ReplaceOne remplace le document entier : il doit porter son
	// environnement, sinon la note sortirait de son environnement.
	n.Env = s.scope.Env
	return replace(ctx, s.Notes, s.key(n.ID), n.ID, n.Version, &n, "note")
}

func (s *MongoStore) DeleteNote(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	return remove(ctx, s.Notes, s.key(id), id, "note")
}

func (s *MongoStore) ListNotes(ctx context.Context, q NoteQuery) ([]Note, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	// Le cloisonnement d'abord : aucune liste ne traverse un environnement.
	and := bson.A{bson.M{"env_id": string(s.scope.Env)}}
	if q.Search != "" {
		// Recherche littérale : le texte saisi n'est jamais une expression
		// régulière (une parenthèse la rendrait invalide).
		re := bson.M{"$regex": regexp.QuoteMeta(q.Search), "$options": "i"}
		and = append(and, bson.M{"$or": bson.A{
			bson.M{"title": re}, bson.M{"body": re}, bson.M{"tags": re},
			bson.M{"blocks.text": re}, bson.M{"blocks.tags": re},
		}})
	}
	if q.Tag != "" {
		// Tag de la note ou d'une de ses boîtes.
		and = append(and, bson.M{"$or": bson.A{bson.M{"tags": q.Tag}, bson.M{"blocks.tags": q.Tag}}})
	}
	if updated := dateRangeFilter(q.UpdatedFrom, q.UpdatedBefore); updated != nil {
		and = append(and, bson.M{"updated_at": updated})
	}
	if q.DocID != "" {
		and = append(and, bson.M{"doc_ids": q.DocID})
	}
	if q.MailID != "" {
		and = append(and, bson.M{"mail_id": q.MailID})
	}
	filter := bson.M{"$and": and}
	opts := options.Find().SetSort(bson.D{{Key: "pinned", Value: -1}, {Key: "updated_at", Value: -1}}).SetLimit(MaxListNotes)
	cur, err := s.Notes.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("notes: list notes: %w", err)
	}
	var out []Note
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("notes: list notes: %w", err)
	}
	return out, nil
}

// dateRangeFilter : l'intervalle semi-ouvert [from, before), ou nil si
// aucune borne n'est donnée.
func dateRangeFilter(from, before time.Time) bson.M {
	out := bson.M{}
	if !from.IsZero() {
		out["$gte"] = from
	}
	if !before.IsZero() {
		out["$lt"] = before
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *MongoStore) CreateTask(ctx context.Context, t Task) error {
	if err := s.ensure(); err != nil {
		return err
	}
	t.Env = s.scope.Env
	t.Version = 1
	if _, err := s.Tasks.InsertOne(ctx, t); err != nil {
		return fmt.Errorf("notes: create task %s: %w", t.ID, err)
	}
	return nil
}

func (s *MongoStore) GetTask(ctx context.Context, id string) (Task, bool, error) {
	if err := s.ensure(); err != nil {
		return Task{}, false, err
	}
	var t Task
	err := s.Tasks.FindOne(ctx, s.key(id)).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("notes: get task %s: %w", id, err)
	}
	return t, true, nil
}

func (s *MongoStore) UpdateTask(ctx context.Context, t Task) error {
	if err := s.ensure(); err != nil {
		return err
	}
	t.Env = s.scope.Env
	return replace(ctx, s.Tasks, s.key(t.ID), t.ID, t.Version, &t, "tâche")
}

func (s *MongoStore) DeleteTask(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	return remove(ctx, s.Tasks, s.key(id), id, "tâche")
}

func (s *MongoStore) ListTasks(ctx context.Context, q TaskQuery) ([]Task, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	filter := bson.M{"env_id": string(s.scope.Env)}
	if q.NoteID != "" {
		filter["note_id"] = q.NoteID
	}
	if q.DocID != "" {
		filter["doc_id"] = q.DocID
	}
	if q.MailID != "" {
		filter["mail_id"] = q.MailID
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(MaxListTasks)
	cur, err := s.Tasks.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("notes: list tasks: %w", err)
	}
	var out []Task
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("notes: list tasks: %w", err)
	}
	return out, nil
}

// replace réécrit un document, à condition que sa version soit encore
// celle qu'on a lue. ReplaceOne remplace tout : sans ce filtre, une
// modification faite entre-temps disparaîtrait sans un mot — exactement la
// classe de bug qui a coûté quatre correctifs à ce projet avec un seul
// utilisateur (jalons 15, 23, 26-27, 39).
//
// setVersion écrit la nouvelle version dans le document remplacé : un
// ReplaceOne ne peut pas combiner $inc, donc c'est l'appelant qui la pose.
func replace(ctx context.Context, c *mongo.Collection, filter bson.M, id string, version int, doc versioned, what string) error {
	filter["version"] = version
	doc.setVersion(version + 1)
	res, err := c.ReplaceOne(ctx, filter, doc)
	if err != nil {
		return fmt.Errorf("notes: update %s %s: %w", what, id, err)
	}
	if res.MatchedCount == 0 {
		// Soit l'entité n'existe pas, soit sa version a bougé. On distingue,
		// parce que « introuvable » et « modifiée entre-temps » ne se
		// traitent pas pareil du tout dans l'interface.
		delete(filter, "version")
		n, cerr := c.CountDocuments(ctx, filter)
		if cerr != nil {
			return fmt.Errorf("notes: update %s %s: %w", what, id, cerr)
		}
		if n == 0 {
			return fmt.Errorf("notes: %s %s introuvable", what, id)
		}
		return fmt.Errorf("%w: %s %s (version %d)", ErrConflict, what, id, version)
	}
	return nil
}

// versioned : une entité dont la version se pose avant écriture.
type versioned interface{ setVersion(int) }

func (n *Note) setVersion(v int) { n.Version = v }
func (t *Task) setVersion(v int) { t.Version = v }

func remove(ctx context.Context, c *mongo.Collection, filter bson.M, id, what string) error {
	res, err := c.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("notes: delete %s %s: %w", what, id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("notes: %s %s introuvable", what, id)
	}
	return nil
}

// MigrateToEnv attribue l'environnement env aux notes et aux tâches qui
// n'en ont pas — celles d'avant le cloisonnement. Idempotente (voir
// webapp.MongoStore.MigrateToEnv pour le raisonnement).
func (s *MongoStore) MigrateToEnv(ctx context.Context, env tenancy.EnvID) (int64, error) {
	if env == "" {
		return 0, fmt.Errorf("notes: migrate: %w", tenancy.ErrNoEnv)
	}
	var total int64
	for _, c := range []*mongo.Collection{s.Notes, s.Tasks} {
		res, err := c.UpdateMany(ctx,
			bson.M{"env_id": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"env_id": string(env)}})
		if err != nil {
			return total, fmt.Errorf("notes: migrate to env %s: %w", env, err)
		}
		total += res.ModifiedCount
	}
	return total, nil
}

// CountUnstamped compte notes et tâches sans environnement.
func (s *MongoStore) CountUnstamped(ctx context.Context) (int64, error) {
	var total int64
	for _, c := range []*mongo.Collection{s.Notes, s.Tasks} {
		n, err := c.CountDocuments(ctx, bson.M{"env_id": bson.M{"$exists": false}})
		if err != nil {
			return total, fmt.Errorf("notes: count unstamped: %w", err)
		}
		total += n
	}
	return total, nil
}
