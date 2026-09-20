package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// MongoStore implémente Store via MongoDB (Atlas en production).
//
// Portée de l'exception à "aucune donnée ne sort de la machine" (voir
// CLAUDE.md) : les documents uploadés via l'interface web (PDF sources
// compris) sont envoyés à Atlas — décision explicite de l'utilisateur,
// pas une fuite accidentelle. `jarvis` (la CLI locale) et
// `internal/store` (persistance JSON sur disque, `jarvis process
// --out-dir`) sont inchangés et n'envoient toujours rien nulle part.
type MongoStore struct {
	Collection *mongo.Collection
}

// NewMongoStore se connecte à uri et retourne un MongoStore prêt à
// l'emploi sur database.collection. Ping échoue tôt (à la construction)
// plutôt qu'au premier job soumis si la base est injoignable.
func NewMongoStore(ctx context.Context, uri, database, collection string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("webapp: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("webapp: ping mongodb: %w", err)
	}
	return &MongoStore{Collection: client.Database(database).Collection(collection)}, nil
}

func (s *MongoStore) Create(ctx context.Context, job Job) (Job, error) {
	doc, err := jobToDoc(job)
	if err != nil {
		return Job{}, fmt.Errorf("webapp: mongo create %s: %w", job.ID, err)
	}
	if _, err := s.Collection.InsertOne(ctx, doc); err != nil {
		return Job{}, fmt.Errorf("webapp: mongo create %s: %w", job.ID, err)
	}
	return job, nil
}

func (s *MongoStore) Get(ctx context.Context, id string) (Job, bool, error) {
	var doc mongoJobDoc
	err := s.Collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("webapp: mongo get %s: %w", id, err)
	}
	job, err := docToJob(doc)
	if err != nil {
		return Job{}, false, fmt.Errorf("webapp: mongo get %s: %w", id, err)
	}
	return job, true, nil
}

// Update ne réécrit que les champs qui changent réellement après création
// (statut, type de document, résultat, erreur, FinishedAt) via $set — pas
// Content : pas de raison de retransmettre le PDF source (potentiellement
// volumineux) à chaque transition de statut.
//
// doc_type fait partie de ce $set depuis la classification automatique
// (jalon 15) : il n'est plus connu à Create (job.DocType == "" à la
// soumission), seulement une fois le job terminé — l'oublier ici laissait
// le champ vide en base pour toujours, alors que le job.templ affichait
// bien le bon type juste après le traitement (lu depuis result_json, pas
// depuis doc_type) : un rechargement de /jobs/{id} perdait silencieusement
// l'information. Trouvé en testant un vrai upload bout en bout, pas en
// relecture.
func (s *MongoStore) Update(ctx context.Context, job Job) error {
	var resultJSON []byte
	if job.Result != nil {
		b, err := json.Marshal(job.Result)
		if err != nil {
			return fmt.Errorf("webapp: mongo update %s: marshal result: %w", job.ID, err)
		}
		resultJSON = b
	}

	update := bson.M{"$set": bson.M{
		"status":      string(job.Status),
		"doc_type":    job.DocType,
		"finished_at": job.FinishedAt,
		"result_json": resultJSON,
		"err":         job.Err,
	}}

	res, err := s.Collection.UpdateByID(ctx, job.ID, update)
	if err != nil {
		return fmt.Errorf("webapp: mongo update %s: %w", job.ID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("webapp: mongo update %s: job not found", job.ID)
	}
	return nil
}

// mongoJobDoc est la représentation BSON d'un Job. Result est stocké tel
// quel en JSON (ResultJSON) plutôt qu'en BSON imbriqué : pipeline.Result
// et ses sous-structs (triage.Result, extraction.Result...) portent des
// json.RawMessage, que le driver BSON ne sait pas transformer
// automatiquement en document imbriqué lisible — le JSON est de toute
// façon déjà le format canonique du projet (mêmes octets que
// internal/store écrit sur disque).
type mongoJobDoc struct {
	ID         string    `bson:"_id"`
	DocType    string    `bson:"doc_type"`
	Filename   string    `bson:"filename"`
	Content    []byte    `bson:"content"`
	Status     string    `bson:"status"`
	CreatedAt  time.Time `bson:"created_at"`
	FinishedAt time.Time `bson:"finished_at,omitempty"`
	ResultJSON []byte    `bson:"result_json,omitempty"`
	Err        string    `bson:"err,omitempty"`
}

func jobToDoc(job Job) (mongoJobDoc, error) {
	doc := mongoJobDoc{
		ID: job.ID, DocType: job.DocType, Filename: job.Filename,
		Content: job.Content, Status: string(job.Status),
		CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt, Err: job.Err,
	}
	if job.Result != nil {
		b, err := json.Marshal(job.Result)
		if err != nil {
			return mongoJobDoc{}, fmt.Errorf("marshal result: %w", err)
		}
		doc.ResultJSON = b
	}
	return doc, nil
}

func docToJob(doc mongoJobDoc) (Job, error) {
	job := Job{
		ID: doc.ID, DocType: doc.DocType, Filename: doc.Filename,
		Content: doc.Content, Status: Status(doc.Status),
		CreatedAt: doc.CreatedAt, FinishedAt: doc.FinishedAt, Err: doc.Err,
	}
	if len(doc.ResultJSON) > 0 {
		var result pipeline.Result
		if err := json.Unmarshal(doc.ResultJSON, &result); err != nil {
			return Job{}, fmt.Errorf("unmarshal result: %w", err)
		}
		job.Result = &result
	}
	return job, nil
}
