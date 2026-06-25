package connectors

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
)

// MySQLSource reads rows from any MySQL database via a configured query. It is a
// generic relational adapter — it has no knowledge of what the rows mean.
type MySQLSource struct {
	DSN   string // user:pass@tcp(host:port)/db
	Query string
	Name  string
	db    *sql.DB
}

func (s *MySQLSource) open() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	db, err := sql.Open("mysql", s.DSN)
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *MySQLSource) name() string {
	if s.Name != "" {
		return s.Name
	}
	return "mysql"
}

func (s *MySQLSource) Discover(ctx context.Context) (SourceSchema, error) {
	db, err := s.open()
	if err != nil {
		return SourceSchema{}, err
	}
	rows, err := db.QueryContext(ctx, s.limited(0))
	if err != nil {
		return SourceSchema{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return SourceSchema{}, err
	}
	fields := make([]Field, len(cols))
	for i, c := range cols {
		fields[i] = Field{Name: c, Type: "text"}
	}
	return SourceSchema{Name: s.name(), Fields: fields}, nil
}

func (s *MySQLSource) Read(ctx context.Context) (Batch, error) {
	db, err := s.open()
	if err != nil {
		return Batch{}, err
	}
	rows, err := db.QueryContext(ctx, s.Query)
	if err != nil {
		return Batch{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Schema: SourceSchema{Name: s.name()}}
	for _, c := range cols {
		batch.Schema.Fields = append(batch.Schema.Fields, Field{Name: c, Type: "text"})
	}
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Batch{}, err
		}
		rec := make(Record, len(cols))
		for i, c := range cols {
			rec[c] = cellString(holders[i])
		}
		batch.Rows = append(batch.Rows, rec)
	}
	return batch, rows.Err()
}

// limited wraps the query with LIMIT n (0 → LIMIT 0, for schema discovery).
func (s *MySQLSource) limited(n int) string {
	return fmt.Sprintf("SELECT * FROM (%s) _q LIMIT %d", s.Query, n)
}

func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
