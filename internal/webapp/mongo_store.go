package webapp

import (
	"bytes"
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
	// Files est le bucket GridFS des fichiers des jobs (original, version
	// PDF, aperçu) — jalon 25 : plus de limite de 16 Mo par document, et
	// Get ne transfère plus le fichier à chaque lecture (le volet d'un
	// document en cours le relisait toutes les 2 s). Nommé
	// "<collection>_files" pour que la collection de test ait le sien.
	Files *mongo.GridFSBucket
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
	db := client.Database(database)
	return &MongoStore{
		Collection: db.Collection(collection),
		Files:      db.GridFSBucket(options.GridFSBucket().SetName(collection + "_files")),
	}, nil
}

func (s *MongoStore) Create(ctx context.Context, job Job) (Job, error) {
	doc, err := jobToDoc(job)
	if err != nil {
		return Job{}, fmt.Errorf("webapp: mongo create %s: %w", job.ID, err)
	}
	// Le fichier d'abord : un job n'existe jamais sans son original. Si
	// l'insertion échoue ensuite, le fichier orphelin est retiré.
	if job.Content != nil {
		if err := s.putFile(ctx, job.ID, FileOriginal, job.Content); err != nil {
			return Job{}, fmt.Errorf("webapp: mongo create %s: %w", job.ID, err)
		}
	}
	if _, err := s.Collection.InsertOne(ctx, doc); err != nil {
		_ = s.deleteFile(ctx, job.ID, FileOriginal)
		return Job{}, fmt.Errorf("webapp: mongo create %s: %w", job.ID, err)
	}
	return job, nil
}

func fileID(id string, name FileName) string { return id + "/" + string(name) }

// putFile remplace le fichier name du job id dans GridFS.
func (s *MongoStore) putFile(ctx context.Context, id string, name FileName, data []byte) error {
	if err := s.deleteFile(ctx, id, name); err != nil {
		return err
	}
	if err := s.Files.UploadFromStreamWithID(ctx, fileID(id, name), string(name), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	return nil
}

func (s *MongoStore) deleteFile(ctx context.Context, id string, name FileName) error {
	err := s.Files.Delete(ctx, fileID(id, name))
	if err != nil && !errors.Is(err, mongo.ErrFileNotFound) {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return nil
}

func (s *MongoStore) WriteFile(ctx context.Context, id string, name FileName, data []byte) error {
	n, err := s.Collection.CountDocuments(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("webapp: mongo write %s/%s: %w", id, name, err)
	}
	if n == 0 {
		return fmt.Errorf("webapp: mongo write %s/%s: job not found", id, name)
	}
	if err := s.putFile(ctx, id, name, data); err != nil {
		return fmt.Errorf("webapp: mongo write %s/%s: %w", id, name, err)
	}
	return nil
}

func (s *MongoStore) ReadFile(ctx context.Context, id string, name FileName) ([]byte, bool, error) {
	var buf bytes.Buffer
	_, err := s.Files.DownloadToStream(ctx, fileID(id, name), &buf)
	if err == nil {
		return buf.Bytes(), true, nil
	}
	if !errors.Is(err, mongo.ErrFileNotFound) {
		return nil, false, fmt.Errorf("webapp: mongo read %s/%s: %w", id, name, err)
	}
	if name != FileOriginal {
		return nil, false, nil
	}
	// Job créé avant le jalon 25 : le PDF est dans le champ "content" du
	// document. Lu tel quel, sans migration.
	var legacy struct {
		Content []byte `bson:"content"`
	}
	err = s.Collection.FindOne(ctx, bson.M{"_id": id}, options.FindOne().SetProjection(bson.M{"content": 1})).Decode(&legacy)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("webapp: mongo read legacy content %s: %w", id, err)
	}
	if len(legacy.Content) == 0 {
		return nil, false, nil
	}
	return legacy.Content, true, nil
}

func (s *MongoStore) Get(ctx context.Context, id string) (Job, bool, error) {
	var doc mongoJobDoc
	err := s.Collection.FindOne(ctx, bson.M{"_id": id}, options.FindOne().SetProjection(bson.M{"content": 0})).Decode(&doc)
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
// (statut, type de document, résultat, erreur, FinishedAt) via
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

// SetTags et SetComment : $set du seul champ concerné (cf. Store).
func (s *MongoStore) SetTags(ctx context.Context, id string, tags []string) error {
	return s.setField(ctx, id, "tags", tags)
}

func (s *MongoStore) SetComment(ctx context.Context, id, comment string) error {
	return s.setField(ctx, id, "comment", comment)
}

func (s *MongoStore) setField(ctx context.Context, id, field string, value any) error {
	res, err := s.Collection.UpdateByID(ctx, id, bson.M{"$set": bson.M{field: value}})
	if err != nil {
		return fmt.Errorf("webapp: mongo set %s %s: %w", field, id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("webapp: mongo set %s %s: job not found", field, id)
	}
	return nil
}

func (s *MongoStore) SetProgress(ctx context.Context, id string, progress *pipeline.Progress) error {
	update := bson.M{"$unset": bson.M{"progress_json": ""}}
	if progress != nil {
		b, err := json.Marshal(progress)
		if err != nil {
			return fmt.Errorf("webapp: mongo set progress %s: marshal: %w", id, err)
		}
		update = bson.M{"$set": bson.M{"progress_json": b}}
	}
	res, err := s.Collection.UpdateByID(ctx, id, update)
	if err != nil {
		return fmt.Errorf("webapp: mongo set progress %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("webapp: mongo set progress %s: job not found", id)
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
	for _, name := range []FileName{FileOriginal, FileRendition, FilePreview} {
		if err := s.deleteFile(ctx, id, name); err != nil {
			return fmt.Errorf("webapp: mongo delete %s: %w", id, err)
		}
	}
	return nil
}

// MigrateComments donne un commentaire vide à chaque document qui n'en a
// pas encore (documents antérieurs au ticket "Ajouter commentaire sur
// document") et retourne le nombre de documents modifiés. Idempotente :
// lancée à chaque démarrage, elle ne touche jamais un commentaire écrit.
func (s *MongoStore) MigrateComments(ctx context.Context) (int64, error) {
	res, err := s.Collection.UpdateMany(ctx,
		bson.M{"comment": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"comment": ""}})
	if err != nil {
		return 0, fmt.Errorf("webapp: mongo migrate comments: %w", err)
	}
	return res.ModifiedCount, nil
}

// List retourne les jobs correspondant à q (bibliothèque de documents,
// jalon 17), triés du plus récent au plus ancien. q.Search filtre sur
// filename/doc_type/tags/comment/search_text (le texte du document — jalon 18,
// "chercher dans les documents") via une regex insensible à la casse —
// pas d'index de recherche plein texte dédié : la collection est petite,
// $regex suffit et reste remplaçable en un jour si le volume grandit.
func (s *MongoStore) List(ctx context.Context, q ListQuery) ([]Job, error) {
	limit := int64(q.Limit)
	if limit == 0 {
		limit = int64(DefaultListLimit)
	}

	var and bson.A
	if q.Search != "" {
		re := bson.M{"$regex": q.Search, "$options": "i"}
		and = append(and, bson.M{"$or": bson.A{
			bson.M{"filename": re},
			bson.M{"doc_type": re},
			bson.M{"tags": re},
			bson.M{"comment": re},
			bson.M{"search_text": re},
		}})
	}
	if q.Status != "" {
		and = append(and, bson.M{"status": string(q.Status)})
	}
	if !q.CreatedFrom.IsZero() {
		and = append(and, bson.M{"created_at": bson.M{"$gte": q.CreatedFrom}})
	}
	if !q.CreatedBefore.IsZero() {
		and = append(and, bson.M{"created_at": bson.M{"$lt": q.CreatedBefore}})
	}
	switch q.Format {
	case "":
	case "pdf":
		// Un job antérieur au jalon 25 n'a pas de format : c'était un PDF.
		and = append(and, bson.M{"$or": bson.A{
			bson.M{"format": "pdf"},
			bson.M{"format": bson.M{"$exists": false}},
			bson.M{"format": ""},
		}})
	default:
		and = append(and, bson.M{"format": q.Format})
	}
	filter := bson.M{}
	if len(and) > 0 {
		filter["$and"] = and
	}

	// Le contenu inline des jobs d'avant le jalon 25 n'est jamais rechargé
	// par une liste.
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit).SetProjection(bson.M{"content": 0})
	if q.SummaryOnly {
		opts.SetProjection(bson.M{"content": 0, "result_json": 0, "thumbnail": 0, "progress_json": 0})
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
	// Comment sans omitempty : un commentaire vide reste un champ présent
	// (critère d'acceptation, cf. MigrateComments).
	Comment    string `bson:"comment"`
	SearchText string `bson:"search_text,omitempty"`
	Thumbnail  []byte `bson:"thumbnail,omitempty"`
	// ProgressJSON : pipeline.Progress sérialisé (jalon 23), même
	// principe que ResultJSON.
	ProgressJSON []byte `bson:"progress_json,omitempty"`

	Format     string `bson:"format,omitempty"`
	MIME       string `bson:"mime,omitempty"`
	Size       int64  `bson:"size,omitempty"`
	SourceHash string `bson:"source_hash,omitempty"`
}

func jobToDoc(job Job) (mongoJobDoc, error) {
	doc := mongoJobDoc{
		ID: job.ID, DocType: job.DocType, Filename: job.Filename,
		Status:    string(job.Status),
		CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Err: job.Err,
		Tags: job.Tags, Comment: job.Comment, SearchText: job.SearchText, Thumbnail: job.Thumbnail,
		Format: job.Format, MIME: job.MIME, Size: job.Size, SourceHash: job.SourceHash,
	}
	if job.Result != nil {
		b, err := json.Marshal(job.Result)
		if err != nil {
			return mongoJobDoc{}, fmt.Errorf("marshal result: %w", err)
		}
		doc.ResultJSON = b
	}
	if job.Progress != nil {
		b, err := json.Marshal(job.Progress)
		if err != nil {
			return mongoJobDoc{}, fmt.Errorf("marshal progress: %w", err)
		}
		doc.ProgressJSON = b
	}
	return doc, nil
}

func docToJob(doc mongoJobDoc) (Job, error) {
	job := Job{
		ID: doc.ID, DocType: doc.DocType, Filename: doc.Filename,
		Status:    Status(doc.Status),
		CreatedAt: doc.CreatedAt, StartedAt: doc.StartedAt, FinishedAt: doc.FinishedAt, Err: doc.Err,
		Tags: doc.Tags, Comment: doc.Comment, SearchText: doc.SearchText, Thumbnail: doc.Thumbnail,
		Format: doc.Format, MIME: doc.MIME, Size: doc.Size, SourceHash: doc.SourceHash,
	}
	if len(doc.ResultJSON) > 0 {
		var result pipeline.Result
		if err := json.Unmarshal(doc.ResultJSON, &result); err != nil {
			return Job{}, fmt.Errorf("unmarshal result: %w", err)
		}
		job.Result = &result
	}
	if len(doc.ProgressJSON) > 0 {
		var progress pipeline.Progress
		if err := json.Unmarshal(doc.ProgressJSON, &progress); err != nil {
			return Job{}, fmt.Errorf("unmarshal progress: %w", err)
		}
		job.Progress = &progress
	}
	return job, nil
}
