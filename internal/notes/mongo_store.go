package notes

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStore persiste notes et tâches dans deux collections MongoDB.
type MongoStore struct {
	Notes *mongo.Collection
	Tasks *mongo.Collection
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
	return &MongoStore{Notes: db.Collection(notesCollection), Tasks: db.Collection(tasksCollection)}, nil
}

func (s *MongoStore) CreateNote(ctx context.Context, n Note) error {
	if _, err := s.Notes.InsertOne(ctx, n); err != nil {
		return fmt.Errorf("notes: create note %s: %w", n.ID, err)
	}
	return nil
}

func (s *MongoStore) GetNote(ctx context.Context, id string) (Note, bool, error) {
	var n Note
	err := s.Notes.FindOne(ctx, bson.M{"_id": id}).Decode(&n)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Note{}, false, nil
	}
	if err != nil {
		return Note{}, false, fmt.Errorf("notes: get note %s: %w", id, err)
	}
	return n, true, nil
}

func (s *MongoStore) UpdateNote(ctx context.Context, n Note) error {
	return replace(ctx, s.Notes, n.ID, n, "note")
}

func (s *MongoStore) DeleteNote(ctx context.Context, id string) error {
	return remove(ctx, s.Notes, id, "note")
}

func (s *MongoStore) ListNotes(ctx context.Context, q NoteQuery) ([]Note, error) {
	var and bson.A
	if q.Search != "" {
		// Recherche littérale : le texte saisi n'est jamais une expression
		// régulière (une parenthèse la rendrait invalide).
		re := bson.M{"$regex": regexp.QuoteMeta(q.Search), "$options": "i"}
		and = append(and, bson.M{"$or": bson.A{bson.M{"title": re}, bson.M{"body": re}, bson.M{"tags": re}}})
	}
	if q.Tag != "" {
		and = append(and, bson.M{"tags": q.Tag})
	}
	if q.DocID != "" {
		and = append(and, bson.M{"doc_ids": q.DocID})
	}
	filter := bson.M{}
	if len(and) > 0 {
		filter["$and"] = and
	}
	opts := options.Find().SetSort(bson.D{{Key: "pinned", Value: -1}, {Key: "updated_at", Value: -1}}).SetLimit(500)
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

func (s *MongoStore) CreateTask(ctx context.Context, t Task) error {
	if _, err := s.Tasks.InsertOne(ctx, t); err != nil {
		return fmt.Errorf("notes: create task %s: %w", t.ID, err)
	}
	return nil
}

func (s *MongoStore) GetTask(ctx context.Context, id string) (Task, bool, error) {
	var t Task
	err := s.Tasks.FindOne(ctx, bson.M{"_id": id}).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("notes: get task %s: %w", id, err)
	}
	return t, true, nil
}

func (s *MongoStore) UpdateTask(ctx context.Context, t Task) error {
	return replace(ctx, s.Tasks, t.ID, t, "tâche")
}

func (s *MongoStore) DeleteTask(ctx context.Context, id string) error {
	return remove(ctx, s.Tasks, id, "tâche")
}

func (s *MongoStore) ListTasks(ctx context.Context, q TaskQuery) ([]Task, error) {
	filter := bson.M{}
	if q.NoteID != "" {
		filter["note_id"] = q.NoteID
	}
	if q.DocID != "" {
		filter["doc_id"] = q.DocID
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(2000)
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

func replace(ctx context.Context, c *mongo.Collection, id string, doc any, what string) error {
	res, err := c.ReplaceOne(ctx, bson.M{"_id": id}, doc)
	if err != nil {
		return fmt.Errorf("notes: update %s %s: %w", what, id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("notes: %s %s introuvable", what, id)
	}
	return nil
}

func remove(ctx context.Context, c *mongo.Collection, id, what string) error {
	res, err := c.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("notes: delete %s %s: %w", what, id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("notes: %s %s introuvable", what, id)
	}
	return nil
}
