package collect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

// MongoStore : trois collections — cibles, seaux de prix (un document par
// jeton et par jour), articles.
type MongoStore struct {
	TargetsC  *mongo.Collection
	PricesC   *mongo.Collection
	ArticlesC *mongo.Collection
}

// NewMongoStore : prefix donne les noms des collections (« market_ » :
// market_targets, market_prices, market_news).
func NewMongoStore(ctx context.Context, uri, database, prefix string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("collect: connect to mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("collect: ping mongodb: %w", err)
	}
	db := client.Database(database)
	s := &MongoStore{TargetsC: db.Collection(prefix + "targets"), PricesC: db.Collection(prefix + "prices"), ArticlesC: db.Collection(prefix + "news")}
	// Index : les articles d'un marché par date, les seaux d'un jeton par
	// jour. Idempotent.
	if _, err := s.ArticlesC.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "markets", Value: 1}, {Key: "published", Value: -1}}}); err != nil {
		return nil, fmt.Errorf("collect: index des articles : %w", err)
	}
	if _, err := s.PricesC.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "token", Value: 1}, {Key: "day", Value: -1}}}); err != nil {
		return nil, fmt.Errorf("collect: index des prix : %w", err)
	}
	return s, nil
}

func (s *MongoStore) UpsertTarget(ctx context.Context, t Target) error {
	set := bson.M{"question": t.Question, "event_title": t.EventTitle, "category": t.Category, "token_id": t.TokenID, "end_date": t.EndDate, "updated_at": t.UpdatedAt}
	_, err := s.TargetsC.UpdateOne(ctx, bson.M{"_id": t.Slug}, bson.M{"$set": set}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("collect: cible %s : %w", t.Slug, err)
	}
	return nil
}

func (s *MongoStore) Targets(ctx context.Context) ([]Target, error) {
	cur, err := s.TargetsC.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("collect: cibles : %w", err)
	}
	var out []Target
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("collect: cibles : %w", err)
	}
	return out, nil
}

func (s *MongoStore) setTarget(ctx context.Context, slug string, set bson.M) error {
	res, err := s.TargetsC.UpdateOne(ctx, bson.M{"_id": slug}, bson.M{"$set": set})
	if err != nil {
		return fmt.Errorf("collect: cible %s : %w", slug, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w : %s", ErrUnknownTarget, slug)
	}
	return nil
}

func (s *MongoStore) SetKeywords(ctx context.Context, slug string, kw []string, model string, at time.Time) error {
	return s.setTarget(ctx, slug, bson.M{"keywords": kw, "keywords_model": model, "keywords_at": at})
}

func (s *MongoStore) SetGDELTChecked(ctx context.Context, slug string, at time.Time) error {
	return s.setTarget(ctx, slug, bson.M{"gdelt_at": at})
}

type pricePoint struct {
	T int64   `bson:"t"`
	P float64 `bson:"p"`
}

type priceBucket struct {
	ID     string       `bson:"_id"`
	Token  string       `bson:"token"`
	Day    time.Time    `bson:"day"`
	Points []pricePoint `bson:"points"`
}

func (b priceBucket) series() []polymarket.PricePoint {
	out := make([]polymarket.PricePoint, len(b.Points))
	for i, p := range b.Points {
		out[i] = polymarket.PricePoint{At: time.Unix(p.T, 0).UTC(), Price: p.P}
	}
	return out
}

// AddPrices lit chaque seau concerné, fusionne et le réécrit. Un seul
// écrivain (la collecte) : la lecture puis l'écriture ne se croisent pas.
func (s *MongoStore) AddPrices(ctx context.Context, tokenID string, pts []polymarket.PricePoint) (int, error) {
	total := 0
	for k, add := range groupByDay(tokenID, pts) {
		var b priceBucket
		err := s.PricesC.FindOne(ctx, bson.M{"_id": k}).Decode(&b)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return total, fmt.Errorf("collect: prix %s : %w", k, err)
		}
		merged, n := mergePoints(b.series(), add)
		if n == 0 {
			continue
		}
		d := add[0].At.UTC()
		b = priceBucket{ID: k, Token: tokenID, Day: time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)}
		for _, p := range merged {
			b.Points = append(b.Points, pricePoint{T: p.At.Unix(), P: p.Price})
		}
		if _, err := s.PricesC.ReplaceOne(ctx, bson.M{"_id": k}, b, options.Replace().SetUpsert(true)); err != nil {
			return total, fmt.Errorf("collect: prix %s : %w", k, err)
		}
		total += n
	}
	return total, nil
}

func (s *MongoStore) Prices(ctx context.Context, tokenID string, from, to time.Time) ([]polymarket.PricePoint, error) {
	cur, err := s.PricesC.Find(ctx, bson.M{"_id": bson.M{"$in": dayKeys(tokenID, from, to)}}, options.Find().SetSort(bson.D{{Key: "day", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("collect: prix : %w", err)
	}
	var bs []priceBucket
	if err := cur.All(ctx, &bs); err != nil {
		return nil, fmt.Errorf("collect: prix : %w", err)
	}
	var out []polymarket.PricePoint
	for _, b := range bs {
		out = append(out, inRange(b.series(), from, to)...)
	}
	return out, nil
}

func (s *MongoStore) LastPrice(ctx context.Context, tokenID string) (time.Time, bool, error) {
	var b priceBucket
	err := s.PricesC.FindOne(ctx, bson.M{"token": tokenID}, options.FindOne().SetSort(bson.D{{Key: "day", Value: -1}})).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("collect: dernier prix : %w", err)
	}
	pts := b.series()
	if len(pts) == 0 {
		return time.Time{}, false, nil
	}
	return pts[len(pts)-1].At, true, nil
}

func (s *MongoStore) UpsertArticles(ctx context.Context, arts []news.Article) (int, error) {
	n := 0
	for _, a := range arts {
		onInsert := bson.M{
			"url": a.URL, "title": a.Title, "summary": a.Summary, "source": a.Source, "lang": a.Lang,
			"via": a.Via, "published": a.Published, "seen": a.Seen, "date_guessed": a.DateGuessed,
		}
		update := bson.M{"$setOnInsert": onInsert, "$addToSet": bson.M{"markets": bson.M{"$each": union(nil, a.Markets)}}}
		res, err := s.ArticlesC.UpdateOne(ctx, bson.M{"_id": a.ID}, update, options.UpdateOne().SetUpsert(true))
		if err != nil {
			return n, fmt.Errorf("collect: article %s : %w", a.ID, err)
		}
		if res.UpsertedCount > 0 {
			n++
		}
	}
	return n, nil
}

func (s *MongoStore) LinkArticles(ctx context.Context, ids []string, slug string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.ArticlesC.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}}, bson.M{"$addToSet": bson.M{"markets": slug}})
	if err != nil {
		return fmt.Errorf("collect: rattacher à %s : %w", slug, err)
	}
	return nil
}

func (s *MongoStore) Articles(ctx context.Context, q ArticleQuery) ([]news.Article, error) {
	filter := bson.M{}
	if q.Market != "" {
		filter["markets"] = q.Market
	}
	pub := bson.M{}
	if !q.From.IsZero() {
		pub["$gte"] = q.From
	}
	if !q.To.IsZero() {
		pub["$lte"] = q.To
	}
	if len(pub) > 0 {
		filter["published"] = pub
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultArticleLimit
	}
	cur, err := s.ArticlesC.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "published", Value: -1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("collect: articles : %w", err)
	}
	var out []news.Article
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("collect: articles : %w", err)
	}
	for i := range out {
		out[i].Published, out[i].Seen = out[i].Published.UTC(), out[i].Seen.UTC()
	}
	return out, nil
}
