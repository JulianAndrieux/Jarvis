package accounts

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

// MongoStore persiste les comptes dans quatre collections.
//
// Volontairement non cloisonné par environnement, contrairement aux
// documents, notes et emails : c'est ce store qui décide qui appartient à
// quel environnement, il ne peut pas dépendre de la réponse.
type MongoStore struct {
	Users    *mongo.Collection
	Envs     *mongo.Collection
	Members  *mongo.Collection
	Sessions *mongo.Collection
}

func NewMongoStore(ctx context.Context, uri, database, prefix string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("accounts: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("accounts: ping mongodb: %w", err)
	}
	db := client.Database(database)
	s := &MongoStore{
		Users:    db.Collection(prefix + "users"),
		Envs:     db.Collection(prefix + "envs"),
		Members:  db.Collection(prefix + "memberships"),
		Sessions: db.Collection(prefix + "sessions"),
	}
	// Une session se retrouve par l'empreinte de son jeton, à chaque
	// requête : c'est l'index qui compte le plus ici. Unique, parce que
	// deux sessions ne peuvent pas partager un jeton.
	if _, err := s.Sessions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "token_hash", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return nil, fmt.Errorf("accounts: index sessions: %w", err)
	}
	// Un couple (environnement, user) ne porte qu'un rôle.
	if _, err := s.Members.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "env_id", Value: 1}, {Key: "user_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return nil, fmt.Errorf("accounts: index memberships: %w", err)
	}
	return s, nil
}

// emailFilter : recherche d'adresse insensible à la casse, littérale (une
// adresse peut contenir un point, qui est un métacaractère).
func emailFilter(email string) bson.M {
	return bson.M{"email": bson.M{"$regex": "^" + regexp.QuoteMeta(email) + "$", "$options": "i"}}
}

func (s *MongoStore) UpsertUser(ctx context.Context, u User) (User, error) {
	// Retrouvé par le compte Google, sinon par l'adresse (invitation créée
	// avant la première connexion). Disabled n'est jamais écrit ici : une
	// connexion ne doit pas réactiver un compte désactivé.
	filters := []bson.M{}
	if u.GoogleSub != "" {
		filters = append(filters, bson.M{"google_sub": u.GoogleSub})
	}
	filters = append(filters, emailFilter(u.Email))
	for _, f := range filters {
		set := bson.M{"email": u.Email, "name": u.Name}
		if u.GoogleSub != "" {
			set["google_sub"] = u.GoogleSub
		}
		var existing User
		err := s.Users.FindOneAndUpdate(ctx, f, bson.M{"$set": set},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&existing)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return User{}, fmt.Errorf("accounts: upsert user %s: %w", u.Email, err)
		}
	}
	if u.ID == "" {
		return User{}, fmt.Errorf("accounts: un nouvel user doit avoir un identifiant")
	}
	if _, err := s.Users.InsertOne(ctx, u); err != nil {
		return User{}, fmt.Errorf("accounts: create user %s: %w", u.Email, err)
	}
	return u, nil
}

func (s *MongoStore) SetUserDisabled(ctx context.Context, id tenancy.UserID, disabled bool) error {
	res, err := s.Users.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"disabled": disabled}})
	if err != nil {
		return fmt.Errorf("accounts: set disabled %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("accounts: user %s introuvable", id)
	}
	return nil
}

func (s *MongoStore) UserByID(ctx context.Context, id tenancy.UserID) (User, bool, error) {
	return s.oneUser(ctx, bson.M{"_id": id}, string(id))
}

func (s *MongoStore) UserByEmail(ctx context.Context, email string) (User, bool, error) {
	return s.oneUser(ctx, emailFilter(email), email)
}

func (s *MongoStore) oneUser(ctx context.Context, filter bson.M, what string) (User, bool, error) {
	var u User
	err := s.Users.FindOne(ctx, filter).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("accounts: get user %s: %w", what, err)
	}
	return u, true, nil
}

func (s *MongoStore) ListUsers(ctx context.Context) ([]User, error) {
	cur, err := s.Users.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "email", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("accounts: list users: %w", err)
	}
	var out []User
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("accounts: list users: %w", err)
	}
	return out, nil
}

func (s *MongoStore) CreateEnv(ctx context.Context, e Environment) error {
	if _, err := s.Envs.InsertOne(ctx, e); err != nil {
		return fmt.Errorf("accounts: create env %s: %w", e.ID, err)
	}
	return nil
}

func (s *MongoStore) Env(ctx context.Context, id tenancy.EnvID) (Environment, bool, error) {
	var e Environment
	err := s.Envs.FindOne(ctx, bson.M{"_id": id}).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Environment{}, false, nil
	}
	if err != nil {
		return Environment{}, false, fmt.Errorf("accounts: get env %s: %w", id, err)
	}
	return e, true, nil
}

func (s *MongoStore) ListEnvs(ctx context.Context) ([]Environment, error) {
	cur, err := s.Envs.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("accounts: list envs: %w", err)
	}
	var out []Environment
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("accounts: list envs: %w", err)
	}
	return out, nil
}

func (s *MongoStore) SetMembership(ctx context.Context, m Membership) error {
	_, err := s.Members.ReplaceOne(ctx,
		bson.M{"env_id": m.Env, "user_id": m.User}, m, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("accounts: set membership %s/%s: %w", m.Env, m.User, err)
	}
	return nil
}

func (s *MongoStore) MembershipsOfUser(ctx context.Context, u tenancy.UserID) ([]Membership, error) {
	return s.memberships(ctx, bson.M{"user_id": u}, bson.D{{Key: "env_id", Value: 1}})
}

func (s *MongoStore) MembershipsOfEnv(ctx context.Context, e tenancy.EnvID) ([]Membership, error) {
	return s.memberships(ctx, bson.M{"env_id": e}, bson.D{{Key: "user_id", Value: 1}})
}

func (s *MongoStore) memberships(ctx context.Context, filter bson.M, sort bson.D) ([]Membership, error) {
	cur, err := s.Members.Find(ctx, filter, options.Find().SetSort(sort))
	if err != nil {
		return nil, fmt.Errorf("accounts: list memberships: %w", err)
	}
	var out []Membership
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("accounts: list memberships: %w", err)
	}
	return out, nil
}

func (s *MongoStore) RemoveMembership(ctx context.Context, e tenancy.EnvID, u tenancy.UserID) error {
	res, err := s.Members.DeleteOne(ctx, bson.M{"env_id": e, "user_id": u})
	if err != nil {
		return fmt.Errorf("accounts: remove membership %s/%s: %w", e, u, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("accounts: appartenance %s/%s introuvable", e, u)
	}
	return nil
}

func (s *MongoStore) CreateSession(ctx context.Context, sess Session) error {
	if _, err := s.Sessions.InsertOne(ctx, sess); err != nil {
		return fmt.Errorf("accounts: create session: %w", err)
	}
	return nil
}

func (s *MongoStore) SessionByTokenHash(ctx context.Context, hash string) (Session, bool, error) {
	var sess Session
	err := s.Sessions.FindOne(ctx, bson.M{"token_hash": hash}).Decode(&sess)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("accounts: get session: %w", err)
	}
	return sess, true, nil
}

// UpdateSession : $set des seuls champs qui bougent — ni le jeton ni le
// user ne sont réécrits (écritures ciblées, comme partout ailleurs dans ce
// projet depuis le jalon 39).
func (s *MongoStore) UpdateSession(ctx context.Context, sess Session) error {
	res, err := s.Sessions.UpdateOne(ctx, bson.M{"_id": sess.ID}, bson.M{"$set": bson.M{
		"env_id":       sess.Env,
		"last_seen_at": sess.LastSeenAt,
		"expires_at":   sess.ExpiresAt,
	}})
	if err != nil {
		return fmt.Errorf("accounts: update session %s: %w", sess.ID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("accounts: session %s introuvable", sess.ID)
	}
	return nil
}

func (s *MongoStore) DeleteSession(ctx context.Context, id tenancy.SessionID) error {
	res, err := s.Sessions.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("accounts: delete session %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("accounts: session %s introuvable", id)
	}
	return nil
}

func (s *MongoStore) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.Sessions.DeleteMany(ctx, bson.M{"expires_at": bson.M{"$lt": before}})
	if err != nil {
		return 0, fmt.Errorf("accounts: purge sessions: %w", err)
	}
	return res.DeletedCount, nil
}
