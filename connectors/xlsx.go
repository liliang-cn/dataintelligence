package connectors

import (
	"context"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// XLSXSource reads one sheet of an .xlsx workbook: a header row becomes the
// schema (with per-column type inference reused from the CSV path), the rows
// below become records. Excel is the first form real SMB data arrives in — a
// pile of workbooks — so this is the workhorse ingest source.
//
// Config knobs (all optional except Path):
//   - Sheet:     which sheet to read; empty → the first sheet.
//   - HeaderRow: 1-based row holding the column names; default 1. SMB sheets
//     routinely put a title/banner in row 1 and the real header in row 2, so
//     `header_row: 2` is a common, necessary override.
type XLSXSource struct {
	Path      string
	Sheet     string
	HeaderRow int // 1-based; 0/1 both mean "row 1"
	Name      string
}

// rows returns the header cells and the data rows below it for the chosen sheet.
func (s *XLSXSource) rows() (sheet string, header []string, data [][]string, err error) {
	f, err := excelize.OpenFile(s.Path)
	if err != nil {
		return "", nil, nil, err
	}
	defer f.Close()

	sheet = s.Sheet
	if sheet == "" {
		sheet = f.GetSheetName(0)
		if sheet == "" {
			return "", nil, nil, fmt.Errorf("xlsx %q has no sheets", s.Path)
		}
	}
	all, err := f.GetRows(sheet)
	if err != nil {
		return "", nil, nil, fmt.Errorf("read sheet %q: %w", sheet, err)
	}
	hr := s.HeaderRow
	if hr < 1 {
		hr = 1
	}
	if len(all) < hr {
		return "", nil, nil, fmt.Errorf("sheet %q has %d rows, header_row=%d out of range", sheet, len(all), hr)
	}
	header = all[hr-1]
	data = all[hr:]
	return sheet, header, data, nil
}

func (s *XLSXSource) name(sheet string) string {
	if s.Name != "" {
		return s.Name
	}
	// file base name + sheet, so multiple sheets of one workbook stay distinct.
	base := baseName(s.Path)
	if sheet != "" && !strings.EqualFold(sheet, "Sheet1") {
		return base + "_" + normalizeName(sheet)
	}
	return base
}

func (s *XLSXSource) Discover(_ context.Context) (SourceSchema, error) {
	sheet, header, data, err := s.rows()
	if err != nil {
		return SourceSchema{}, err
	}
	fields := make([]Field, len(header))
	for i, h := range header {
		col := make([]string, 0, len(data))
		for _, row := range data {
			if i < len(row) {
				col = append(col, row[i])
			}
		}
		fields[i] = Field{Name: h, Type: inferType(col)}
	}
	return SourceSchema{Name: s.name(sheet), Fields: fields}, nil
}

func (s *XLSXSource) Read(ctx context.Context) (Batch, error) {
	schema, err := s.Discover(ctx)
	if err != nil {
		return Batch{}, err
	}
	_, header, data, err := s.rows()
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Schema: schema}
	for _, row := range data {
		rec := make(Record, len(header))
		for i, h := range header {
			if h == "" {
				continue // skip blank header columns (trailing empties in Excel)
			}
			if i < len(row) {
				rec[h] = row[i]
			} else {
				rec[h] = ""
			}
		}
		batch.Rows = append(batch.Rows, rec)
	}
	return batch, nil
}

// normalizeName lower-cases and underscores a sheet name for use in a source id.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
