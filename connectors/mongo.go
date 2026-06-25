package connectors

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoSource reads documents from any MongoDB collection and flattens top-level
// fields into Records. Generic document adapter.
type MongoSource struct {
	URI        string
	Database   string
	Collection string
	Limit      int
	cli        *mongo.Client
}

func (s *MongoSource) connect(ctx context.Context) (*mongo.Client, error) {
	if s.cli != nil {
		return s.cli, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cli, err := mongo.Connect(ctx, options.Client().ApplyURI(s.URI))
	if err != nil {
		return nil, err
	}
	s.cli = cli
	return cli, nil
}

func (s *MongoSource) Discover(_ context.Context) (SourceSchema, error) {
	return SourceSchema{Name: s.Collection}, nil
}

func (s *MongoSource) Read(ctx context.Context) (Batch, error) {
	cli, err := s.connect(ctx)
	if err != nil {
		return Batch{}, err
	}
	limit := int64(s.Limit)
	if limit <= 0 {
		limit = 1000
	}
	cur, err := cli.Database(s.Database).Collection(s.Collection).Find(ctx, bson.M{}, options.Find().SetLimit(limit))
	if err != nil {
		return Batch{}, err
	}
	defer cur.Close(ctx)

	batch := Batch{Schema: SourceSchema{Name: s.Collection}}
	seen := map[string]bool{}
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return Batch{}, err
		}
		rec := make(Record, len(doc))
		for k, v := range doc {
			rec[k] = mongoVal(v)
			seen[k] = true
		}
		batch.Rows = append(batch.Rows, rec)
	}
	for k := range seen {
		batch.Schema.Fields = append(batch.Schema.Fields, Field{Name: k, Type: "text"})
	}
	return batch, cur.Err()
}

func mongoVal(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
