package connectors

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// RedisSource reads key/value (and hash) entries matching a pattern from any
// Redis. Generic KV adapter — pattern + decoding are config, not business logic.
type RedisSource struct {
	Addr     string
	Password string
	Match    string // SCAN MATCH pattern, e.g. "stock:*"
	Name     string
	rdb      *redis.Client
}

func (s *RedisSource) client() *redis.Client {
	if s.rdb == nil {
		s.rdb = redis.NewClient(&redis.Options{Addr: s.Addr, Password: s.Password})
	}
	return s.rdb
}

func (s *RedisSource) nm() string {
	if s.Name != "" {
		return s.Name
	}
	return "redis"
}

func (s *RedisSource) Discover(_ context.Context) (SourceSchema, error) {
	return SourceSchema{Name: s.nm(), Fields: []Field{{Name: "key", Type: "text"}, {Name: "value", Type: "text"}}}, nil
}

// Read SCANs matching keys and reads each. Hashes are flattened into the Record;
// scalars land under "value".
func (s *RedisSource) Read(ctx context.Context) (Batch, error) {
	rdb := s.client()
	match := s.Match
	if match == "" {
		match = "*"
	}
	batch := Batch{Schema: SourceSchema{Name: s.nm()}}
	keyFields := map[string]bool{"key": true}
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, match, 200).Result()
		if err != nil {
			return Batch{}, err
		}
		for _, k := range keys {
			rec := Record{"key": k}
			switch rdb.Type(ctx, k).Val() {
			case "hash":
				h, _ := rdb.HGetAll(ctx, k).Result()
				for f, v := range h {
					rec[f] = v
					keyFields[f] = true
				}
			default:
				rec["value"], _ = rdb.Get(ctx, k).Result()
				keyFields["value"] = true
			}
			batch.Rows = append(batch.Rows, rec)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	for f := range keyFields {
		batch.Schema.Fields = append(batch.Schema.Fields, Field{Name: f, Type: "text"})
	}
	return batch, nil
}
