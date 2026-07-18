package connectors

import "testing"

// TestBuildMSSQLRouting verifies both the "sqlserver" and "mssql" aliases route
// to a configured *MSSQLSource. It does not open a connection (no live server).
func TestBuildMSSQLRouting(t *testing.T) {
	for _, typ := range []string{"sqlserver", "mssql"} {
		s, err := Build(SourceSpec{
			Name:   "erp",
			Type:   typ,
			Config: map[string]string{"dsn": "sqlserver://u:p@h:1433?database=db", "query": "SELECT 1"},
		})
		if err != nil {
			t.Fatalf("build %s: %v", typ, err)
		}
		ms, ok := s.(*MSSQLSource)
		if !ok {
			t.Fatalf("%s: want *MSSQLSource, got %T", typ, s)
		}
		if ms.DSN != "sqlserver://u:p@h:1433?database=db" {
			t.Fatalf("%s: dsn = %q", typ, ms.DSN)
		}
		if ms.Query != "SELECT 1" {
			t.Fatalf("%s: query = %q", typ, ms.Query)
		}
	}
}
