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
// (statut, type de document, résultat, erreur, tags, FinishedAt) via
// $set — pas Content : pas de raison de retransmettre le PDF source
// (potentiellement volumineux) à chaque transition de statut.
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
		"started_at":  job.StartedAt,
		"finished_at": job.FinishedAt,
		"result_json": resultJSON,
		"err":         job.Err,
		"tags":        job.Tags,
		"search_text": job.SearchText,
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

// Delete supprime définitivement le job id (bibliothèque de documents,
// jalon 18).
func (s *MongoStore) SetThumbnail(ctx context.Context, id string, png []byte) error {
	res, err := s.Collection.UpdateByID(ctx, id, bson.M{"$set": bson.M{"thumbnail": png}})
	if err != nil {
		return fmt.Errorf("webapp: mongo set thumbnail %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("webapp: mongo set thumbnail %s: job not found", id)
	}
	return nil
}

func (s *MongoStore) Delete(ctx context.Context, id string) error {
	res, err := s.Collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("webapp: mongo delete %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("webapp: mongo delete %s: job not found", id)
	}
	return nil
}

// List retourne les jobs correspondant à q (bibliothèque de documents,
// jalon 17), triés du plus récent au plus ancien. q.Search filtre sur
// filename/doc_type/tags/search_text (le texte du document — jalon 18,
// "chercher dans les documents") via une regex insensible à la casse —
// pas d'index de recherche plein texte dédié : la collection est petite,
// $regex suffit et reste remplaçable en un jour si le volume grandit.
func (s *MongoStore) List(ctx context.Context, q ListQuery) ([]Job, error) {
	limit := int64(q.Limit)
	if limit == 0 {
		limit = int64(DefaultListLimit)
	}

	filter := bson.M{}
	if q.Search != "" {
		re := bson.M{"$regex": q.Search, "$options": "i"}
		filter["$or"] = bson.A{
			bson.M{"filename": re},
			bson.M{"doc_type": re},
			bson.M{"tags": re},
			bson.M{"search_text": re},
		}
	}
	if q.Status != "" {
		filter["status"] = string(q.Status)
	}

	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit)
	if q.SummaryOnly {
		opts.SetProjection(bson.M{"content": 0, "result_json": 0, "thumbnail": 0})
	}
	cur, err := s.Collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("webapp: mongo list: %w", err)
	}
	defer cur.Close(ctx)

	var jobs []Job
	for cur.Next(ctx) {
		var doc mongoJobDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("webapp: mongo list: decode: %w", err)
		}
		job, err := docToJob(doc)
		if err != nil {
			return nil, fmt.Errorf("webapp: mongo list: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("webapp: mongo list: %w", err)
	}
	return jobs, nil
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
	StartedAt  time.Time `bson:"started_at,omitempty"`
	FinishedAt time.Time `bson:"finished_at,omitempty"`
	ResultJSON []byte    `bson:"result_json,omitempty"`
	Err        string    `bson:"err,omitempty"`
	Tags       []string  `bson:"tags,omitempty"`
	SearchText string    `bson:"search_text,omitempty"`
	Thumbnail  []byte    `bson:"thumbnail,omitempty"`
}

func jobToDoc(job Job) (mongoJobDoc, error) {
	doc := mongoJobDoc{
		ID: job.ID, DocType: job.DocType, Filename: job.Filename,
		Content: job.Content, Status: string(job.Status),
		CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Err: job.Err,
		Tags: job.Tags, SearchText: job.SearchText, Thumbnail: job.Thumbnail,
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
		CreatedAt: doc.CreatedAt, StartedAt: doc.StartedAt, FinishedAt: doc.FinishedAt, Err: doc.Err,
		Tags: doc.Tags, SearchText: doc.SearchText, Thumbnail: doc.Thumbnail,
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
