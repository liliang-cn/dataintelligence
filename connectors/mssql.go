package connectors

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/microsoft/go-mssqldb"
)

// MSSQLSource reads rows from any SQL Server database via a configured query. It
// is a generic relational adapter — it has no knowledge of what the rows mean.
// Chinese SMB ERPs (用友/金蝶) commonly run on SQL Server.
type MSSQLSource struct {
	DSN   string // sqlserver://user:pass@host:port?database=db
	Query string
	Name  string
	db    *sql.DB
}

func (s *MSSQLSource) open() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	db, err := sql.Open("sqlserver", s.DSN)
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *MSSQLSource) name() string {
	if s.Name != "" {
		return s.Name
	}
	return "mssql"
}

func (s *MSSQLSource) Discover(ctx context.Context) (SourceSchema, error) {
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

func (s *MSSQLSource) Read(ctx context.Context) (Batch, error) {
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

// limited wraps the query for schema discovery. SQL Server has no LIMIT, so we
// use TOP 0 to yield the columns with zero rows. The subquery alias needs AS.
func (s *MSSQLSource) limited(n int) string {
	return fmt.Sprintf("SELECT TOP %d * FROM (%s) AS _q", n, s.Query)
}
