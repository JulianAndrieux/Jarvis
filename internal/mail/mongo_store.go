package mail

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStore persiste les emails dans une collection, le contenu des
// pièces jointes dans une seconde (un document par pièce jointe : un
// email et ses pièces jointes dépasseraient vite les 16 Mo d'un document).
type MongoStore struct {
	Mails *mongo.Collection
	Files *mongo.Collection
}

func NewMongoStore(ctx context.Context, uri, database, mailsCollection, filesCollection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mail: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("mail: ping mongodb: %w", err)
	}
	db := client.Database(database)
	s := &MongoStore{Mails: db.Collection(mailsCollection), Files: db.Collection(filesCollection)}
	// Index de la relève (dernier UID) et de la liste (par date).
	if _, err := s.Mails.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "account", Value: 1}, {Key: "mailbox", Value: 1}, {Key: "uid_validity", Value: 1}, {Key: "uid", Value: -1}}},
		{Keys: bson.D{{Key: "date", Value: -1}}},
	}); err != nil {
		return nil, fmt.Errorf("mail: index: %w", err)
	}
	return s, nil
}

type fileDoc struct {
	ID   string `bson:"_id"`
	Data []byte `bson:"data"`
}

func (s *MongoStore) Save(ctx context.Context, m Mail, files map[int][]byte) (bool, error) {
	n, err := s.Mails.CountDocuments(ctx, bson.M{"_id": m.ID}, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("mail: save %s: %w", m.ID, err)
	}
	if n > 0 {
		return false, nil
	}
	// Pièces jointes d'abord : un email enregistré a toujours les siennes.
	for i, data := range files {
		key := fileKey(m.ID, i)
		if _, err := s.Files.ReplaceOne(ctx, bson.M{"_id": key}, fileDoc{ID: key, Data: data}, options.Replace().SetUpsert(true)); err != nil {
			return false, fmt.Errorf("mail: pièce jointe %s: %w", key, err)
		}
	}
	if _, err := s.Mails.InsertOne(ctx, m); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("mail: save %s: %w", m.ID, err)
	}
	return true, nil
}

func (s *MongoStore) Get(ctx context.Context, id string) (Mail, bool, error) {
	var m Mail
	err := s.Mails.FindOne(ctx, bson.M{"_id": id}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Mail{}, false, nil
	}
	if err != nil {
		return Mail{}, false, fmt.Errorf("mail: get %s: %w", id, err)
	}
	return m, true, nil
}

// filter : le filtre MongoDB de q.
func filter(q Query) bson.M {
	var and bson.A
	if q.Search != "" {
		// Recherche littérale : le texte saisi n'est jamais une expression
		// régulière.
		re := bson.M{"$regex": regexp.QuoteMeta(q.Search), "$options": "i"}
		and = append(and, bson.M{"$or": bson.A{
			bson.M{"subject": re}, bson.M{"from.name": re}, bson.M{"from.email": re},
			bson.M{"text": re}, bson.M{"triage.summary": re},
		}})
	}
	if q.Category != "" {
		and = append(and, bson.M{"triage.category": q.Category})
	}
	if q.Reply {
		and = append(and, bson.M{"triage.reply": true})
	}
	if q.Untriaged {
		// $not/$gte : un tri enregistré avant la version 2 n'a pas le champ.
		and = append(and, bson.M{"$or": bson.A{
			bson.M{"triage.category": "", "triage.error": ""},
			bson.M{"triage.version": bson.M{"$not": bson.M{"$gte": TriageVersion}}},
		}})
	}
	f := bson.M{}
	if len(and) > 0 {
		f["$and"] = and
	}
	return f
}

func (s *MongoStore) Count(ctx context.Context, q Query) (int, error) {
	n, err := s.Mails.CountDocuments(ctx, filter(q))
	if err != nil {
		return 0, fmt.Errorf("mail: count: %w", err)
	}
	return int(n), nil
}

func (s *MongoStore) List(ctx context.Context, q Query) ([]Mail, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	opts := options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit))
	cur, err := s.Mails.Find(ctx, filter(q), opts)
	if err != nil {
		return nil, fmt.Errorf("mail: list: %w", err)
	}
	var out []Mail
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("mail: list: %w", err)
	}
	return out, nil
}

func (s *MongoStore) SetTriage(ctx context.Context, id string, t Triage) error {
	res, err := s.Mails.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"triage": t}})
	if err != nil {
		return fmt.Errorf("mail: tri de %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("mail: email %s introuvable", id)
	}
	return nil
}

func (s *MongoStore) SetAttachmentDoc(ctx context.Context, id string, index int, docID string) error {
	res, err := s.Mails.UpdateOne(ctx,
		bson.M{"_id": id, "attachments.index": index},
		bson.M{"$set": bson.M{"attachments.$.doc_id": docID}})
	if err != nil {
		return fmt.Errorf("mail: pièce jointe %d de %s: %w", index, id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("mail: pièce jointe %d de %s introuvable", index, id)
	}
	return nil
}

func (s *MongoStore) Attachment(ctx context.Context, id string, index int) ([]byte, bool, error) {
	var f fileDoc
	err := s.Files.FindOne(ctx, bson.M{"_id": fileKey(id, index)}).Decode(&f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("mail: pièce jointe %d de %s: %w", index, id, err)
	}
	return f.Data, true, nil
}

func (s *MongoStore) LastUID(ctx context.Context, account, mailbox string, uidValidity uint32) (uint32, error) {
	var m struct {
		UID uint32 `bson:"uid"`
	}
	err := s.Mails.FindOne(ctx,
		bson.M{"account": account, "mailbox": mailbox, "uid_validity": uidValidity},
		options.FindOne().SetSort(bson.D{{Key: "uid", Value: -1}}).SetProjection(bson.M{"uid": 1}),
	).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mail: dernier UID: %w", err)
	}
	return m.UID, nil
}
