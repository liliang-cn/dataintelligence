// Package connectors reads data from sources (and, later, writes to sinks).
// A Source discovers its structure and yields records; this is the left edge of
// the data plane (whiteboard: Sources → Ingest → ...).
package connectors

import "context"

// Field is one discovered column with an inferred type.
type Field struct {
	Name string
	Type string // text | int | numeric | date | bool
}

// SourceSchema is a source's discovered shape (powers mapping + AI Diff).
type SourceSchema struct {
	Name   string
	Fields []Field
}

// Record is one row keyed by source field name.
type Record map[string]string

// Batch is a schema + its rows.
type Batch struct {
	Schema SourceSchema
	Rows   []Record
}

// Source is a readable data source.
type Source interface {
	Discover(ctx context.Context) (SourceSchema, error)
	Read(ctx context.Context) (Batch, error)
}

// ChangeEvent is one CDC change (insert/update/delete) with a monotonic cursor.
type ChangeEvent struct {
	Op     string // insert | update | delete
	Cursor int64
	Record Record
}

// ChangeSource is a streaming/CDC source: it emits changes as they happen.
type ChangeSource interface {
	Source
	Subscribe(ctx context.Context) (<-chan ChangeEvent, error)
}
