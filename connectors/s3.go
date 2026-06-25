package connectors

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Source reads objects under a bucket/prefix from any S3-compatible store
// (AWS, MinIO, …) and parses CSV/JSON files into Records. Generic object/file
// adapter — the data-lake layout is config, not code.
type S3Source struct {
	Endpoint  string // host:port (no scheme)
	AccessKey string
	SecretKey string
	Bucket    string
	Prefix    string
	UseSSL    bool
	Name      string
	cli       *minio.Client
}

func (s *S3Source) client() (*minio.Client, error) {
	if s.cli != nil {
		return s.cli, nil
	}
	c, err := minio.New(s.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""),
		Secure: s.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	s.cli = c
	return c, nil
}

func (s *S3Source) nm() string {
	if s.Name != "" {
		return s.Name
	}
	return "s3:" + s.Bucket
}

func (s *S3Source) Discover(_ context.Context) (SourceSchema, error) {
	return SourceSchema{Name: s.nm()}, nil
}

// Read lists objects under the prefix and parses each (CSV → rows, JSON/JSONL →
// rows). The source file name rides along as _object for lineage.
func (s *S3Source) Read(ctx context.Context) (Batch, error) {
	cli, err := s.client()
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Schema: SourceSchema{Name: s.nm()}}
	seen := map[string]bool{"_object": true}
	for obj := range cli.ListObjects(ctx, s.Bucket, minio.ListObjectsOptions{Prefix: s.Prefix, Recursive: true}) {
		if obj.Err != nil {
			return Batch{}, obj.Err
		}
		o, err := cli.GetObject(ctx, s.Bucket, obj.Key, minio.GetObjectOptions{})
		if err != nil {
			return Batch{}, err
		}
		data, err := io.ReadAll(o)
		o.Close()
		if err != nil {
			return Batch{}, err
		}
		recs := parseObject(obj.Key, data)
		for _, r := range recs {
			for k := range r {
				seen[k] = true
			}
			batch.Rows = append(batch.Rows, r)
		}
	}
	for k := range seen {
		batch.Schema.Fields = append(batch.Schema.Fields, Field{Name: k, Type: "text"})
	}
	return batch, nil
}

// parseObject turns a CSV or JSON(L) file into Records, tagging each with _object.
func parseObject(key string, data []byte) []Record {
	switch {
	case strings.HasSuffix(key, ".csv"):
		r := csv.NewReader(strings.NewReader(string(data)))
		rows, err := r.ReadAll()
		if err != nil || len(rows) < 2 {
			return nil
		}
		head := rows[0]
		var out []Record
		for _, row := range rows[1:] {
			rec := Record{"_object": key}
			for i, h := range head {
				if i < len(row) {
					rec[h] = row[i]
				}
			}
			out = append(out, rec)
		}
		return out
	case strings.HasSuffix(key, ".jsonl"):
		var out []Record
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			out = append(out, jsonToRecord(key, []byte(line)))
		}
		return out
	case strings.HasSuffix(key, ".json"):
		var arr []map[string]any
		if json.Unmarshal(data, &arr) == nil {
			var out []Record
			for _, m := range arr {
				out = append(out, mapToRecord(key, m))
			}
			return out
		}
		return []Record{jsonToRecord(key, data)}
	}
	return nil
}

func jsonToRecord(key string, b []byte) Record {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return mapToRecord(key, m)
}

func mapToRecord(key string, m map[string]any) Record {
	rec := Record{"_object": key}
	for k, v := range m {
		rec[k] = str(v)
	}
	return rec
}
