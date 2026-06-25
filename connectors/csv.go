package connectors

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// CSVSource reads a CSV file: the header becomes the schema (with per-column type
// inference), the rows become records.
type CSVSource struct {
	Path string
	Name string // logical source name (defaults to file base name)
}

func (s *CSVSource) read() (header []string, rows [][]string, err error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	header, err = r.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, rec)
	}
	return header, rows, nil
}

func (s *CSVSource) Discover(_ context.Context) (SourceSchema, error) {
	header, rows, err := s.read()
	if err != nil {
		return SourceSchema{}, err
	}
	name := s.Name
	if name == "" {
		name = baseName(s.Path)
	}
	fields := make([]Field, len(header))
	for i, h := range header {
		col := make([]string, 0, len(rows))
		for _, row := range rows {
			if i < len(row) {
				col = append(col, row[i])
			}
		}
		fields[i] = Field{Name: h, Type: inferType(col)}
	}
	return SourceSchema{Name: name, Fields: fields}, nil
}

func (s *CSVSource) Read(ctx context.Context) (Batch, error) {
	schema, err := s.Discover(ctx)
	if err != nil {
		return Batch{}, err
	}
	header, rows, err := s.read()
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Schema: schema}
	for _, row := range rows {
		rec := make(Record, len(header))
		for i, h := range header {
			if i < len(row) {
				rec[h] = row[i]
			}
		}
		batch.Rows = append(batch.Rows, rec)
	}
	return batch, nil
}

// inferType samples non-empty values and picks the narrowest type that fits all.
func inferType(values []string) string {
	allInt, allFloat, allDate, allBool, n := true, true, true, true, 0
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		n++
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			allInt = false
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			allFloat = false
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			allDate = false
		}
		if lv := strings.ToLower(v); lv != "true" && lv != "false" {
			allBool = false
		}
	}
	switch {
	case n == 0:
		return "text"
	case allInt:
		return "int"
	case allFloat:
		return "numeric"
	case allDate:
		return "date"
	case allBool:
		return "bool"
	default:
		return "text"
	}
}

func baseName(path string) string {
	p := path
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.LastIndexByte(p, '.'); i > 0 {
		p = p[:i]
	}
	return p
}
