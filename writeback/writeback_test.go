package writeback

import (
	"strings"
	"testing"
)

func testSchema() *Schema {
	return &Schema{
		Tables: []WritableTable{
			{
				Name: "orders", PrimaryKey: "order_id", AllowUpdate: true,
				RequirePredicate: true, MaxAffected: 50,
				Columns: []Column{{Name: "status", Type: "text", Enum: []string{"completed", "returned", "refunded"}}},
			},
			{
				Name: "refunds", PrimaryKey: "refund_id", AllowInsert: true, MaxAffected: 1,
				Columns: []Column{
					{Name: "refund_id", Type: "int", Required: true},
					{Name: "order_id", Type: "int", Required: true},
					{Name: "refund_amount", Type: "numeric", Required: true},
				},
			},
		},
		Governance: Governance{ProposerRoles: []string{"analyst", "admin"}, ApproverRoles: []string{"admin"}},
	}
}

func TestValidateRejectsNonWritableTable(t *testing.T) {
	s := testSchema()
	d := &DataChange{Op: OpUpdate, Table: "suppliers", Set: map[string]any{"country": "X"}, Where: []Predicate{{"supplier_id", "=", 1}}}
	if err := d.Validate(s); err == nil {
		t.Fatal("non-writable table must be rejected")
	}
}

func TestValidateRejectsDisallowedOp(t *testing.T) {
	s := testSchema()
	d := &DataChange{Op: OpDelete, Table: "orders", Where: []Predicate{{"order_id", "=", 1}}}
	if err := d.Validate(s); err == nil {
		t.Fatal("delete on orders (update-only) must be rejected")
	}
}

func TestValidateRequiresPredicate(t *testing.T) {
	s := testSchema()
	d := &DataChange{Op: OpUpdate, Table: "orders", Set: map[string]any{"status": "returned"}}
	if err := d.Validate(s); err == nil || !strings.Contains(err.Error(), "predicate") {
		t.Fatalf("unbounded update must be refused, got %v", err)
	}
}

func TestValidateEnumAndType(t *testing.T) {
	s := testSchema()
	bad := &DataChange{Op: OpUpdate, Table: "orders", Set: map[string]any{"status": "frozen"}, Where: []Predicate{{"order_id", "=", 1}}}
	if err := bad.Validate(s); err == nil {
		t.Fatal("value outside the enum must be rejected")
	}
	badType := &DataChange{Op: OpInsert, Table: "refunds", Set: map[string]any{"refund_id": "abc", "order_id": 1, "refund_amount": 9.5}}
	if err := badType.Validate(s); err == nil {
		t.Fatal("non-int for an int column must be rejected")
	}
}

func TestValidateInsertRequiresRequiredColumns(t *testing.T) {
	s := testSchema()
	d := &DataChange{Op: OpInsert, Table: "refunds", Set: map[string]any{"refund_id": 1}} // missing order_id, refund_amount
	if err := d.Validate(s); err == nil {
		t.Fatal("insert missing required columns must be rejected")
	}
}

func TestValidateRejectsNonWritableColumn(t *testing.T) {
	s := testSchema()
	d := &DataChange{Op: OpUpdate, Table: "orders", Set: map[string]any{"customer_id": 9}, Where: []Predicate{{"order_id", "=", 1}}}
	if err := d.Validate(s); err == nil {
		t.Fatal("writing a column not on the allowlist must be rejected")
	}
}

func TestBuildParameterizedSQL(t *testing.T) {
	up := &DataChange{Op: OpUpdate, Table: "orders", Set: map[string]any{"status": "returned"}, Where: []Predicate{{"order_id", "=", 1}}}
	sql, args, err := build(up)
	if err != nil {
		t.Fatal(err)
	}
	if sql != `UPDATE "orders" SET "status" = $1 WHERE "order_id" = $2` {
		t.Fatalf("update SQL = %q", sql)
	}
	if len(args) != 2 || args[0] != "returned" {
		t.Fatalf("args = %v", args)
	}

	del := &DataChange{Op: OpDelete, Table: "refunds", Where: []Predicate{{"refund_id", "in", []any{1, 2, 3}}}}
	sql, args, err = build(del)
	if err != nil {
		t.Fatal(err)
	}
	if sql != `DELETE FROM "refunds" WHERE "refund_id" IN ($1, $2, $3)` || len(args) != 3 {
		t.Fatalf("delete-in SQL = %q args=%v", sql, args)
	}

	ins := &DataChange{Op: OpInsert, Table: "refunds", Set: map[string]any{"refund_id": 9, "order_id": 1, "refund_amount": 5.0}}
	sql, _, err = build(ins)
	if err != nil {
		t.Fatal(err)
	}
	// columns are sorted for deterministic SQL
	if !strings.HasPrefix(sql, `INSERT INTO "refunds" ("order_id", "refund_amount", "refund_id") VALUES (`) {
		t.Fatalf("insert SQL = %q", sql)
	}
}

func TestGovernanceRoles(t *testing.T) {
	s := testSchema()
	if !s.CanPropose("analyst") || s.CanApprove("analyst") {
		t.Fatal("analyst may propose but not approve")
	}
	if !s.CanApprove("admin") {
		t.Fatal("admin may approve")
	}
}

func TestRedLine(t *testing.T) {
	if redLineViolation(`UPDATE "orders" SET "status"=$1 WHERE "order_id"=$2`) != "" {
		t.Fatal("a clean parameterized update should pass the red-line")
	}
	for _, bad := range []string{
		`DROP TABLE orders`,
		`UPDATE orders SET x=1; DELETE FROM orders`,
		`TRUNCATE refunds`,
	} {
		if redLineViolation(bad) == "" {
			t.Fatalf("red-line should block %q", bad)
		}
	}
}
