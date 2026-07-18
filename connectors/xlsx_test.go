package connectors

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

// writeXLSX creates a temp workbook and returns its path. `rows` is written to
// `sheet` starting at A1.
func writeXLSX(t *testing.T, sheet string, rows [][]string) string {
	t.Helper()
	f := excelize.NewFile()
	// Rename the default sheet to the one we want.
	if sheet != "Sheet1" {
		if err := f.SetSheetName("Sheet1", sheet); err != nil {
			t.Fatalf("rename sheet: %v", err)
		}
	}
	for r, row := range rows {
		for c, val := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellStr(sheet, cell, val); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "book.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

func TestXLSXSource_DiscoverAndRead(t *testing.T) {
	path := writeXLSX(t, "Sheet1", [][]string{
		{"order_id", "amount", "region"},
		{"1", "12.50", "west"},
		{"2", "8", "east"},
	})
	src := &XLSXSource{Path: path}

	schema, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(schema.Fields) != 3 {
		t.Fatalf("want 3 fields, got %d", len(schema.Fields))
	}
	// amount is 12.50/8 → numeric; order_id 1/2 → int.
	byName := map[string]string{}
	for _, f := range schema.Fields {
		byName[f.Name] = f.Type
	}
	if byName["order_id"] != "int" {
		t.Errorf("order_id type = %q, want int", byName["order_id"])
	}
	if byName["amount"] != "numeric" {
		t.Errorf("amount type = %q, want numeric", byName["amount"])
	}

	batch, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(batch.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(batch.Rows))
	}
	if batch.Rows[0]["region"] != "west" || batch.Rows[1]["amount"] != "8" {
		t.Errorf("unexpected rows: %+v", batch.Rows)
	}
}

// The common SMB shape: a title/banner in row 1, real header in row 2.
func TestXLSXSource_HeaderRowOffset(t *testing.T) {
	path := writeXLSX(t, "销售", [][]string{
		{"2026年门店销售明细", "", ""}, // banner row
		{"门店", "品类", "金额"},        // real header (row 2)
		{"西单店", "厨电", "1200"},
		{"中关村店", "手机", "3400"},
	})
	src := &XLSXSource{Path: path, Sheet: "销售", HeaderRow: 2}

	batch, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(batch.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(batch.Rows))
	}
	if batch.Rows[0]["门店"] != "西单店" || batch.Rows[0]["金额"] != "1200" {
		t.Errorf("unexpected first row: %+v", batch.Rows[0])
	}
}

// Build() from a manifest spec should produce a working XLSXSource.
func TestBuild_XLSX(t *testing.T) {
	path := writeXLSX(t, "Sheet1", [][]string{
		{"k", "v"},
		{"a", "1"},
	})
	src, err := Build(SourceSpec{
		Name:   "book",
		Type:   "xlsx",
		Config: map[string]string{"path": path, "header_row": "1"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	batch, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(batch.Rows) != 1 || batch.Rows[0]["v"] != "1" {
		t.Errorf("unexpected batch: %+v", batch.Rows)
	}
}
