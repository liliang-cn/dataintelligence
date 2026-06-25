package connectors

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestLoadEnvExpandAndBuild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	os.Setenv("TEST_S3_KEY", "secret-from-env")
	yaml := `
sources:
  - name: orders
    type: mysql
    config: {dsn: "u:p@tcp(h:3306)/db", query: "SELECT 1"}
  - name: lake
    type: s3
    config: {endpoint: "h:9000", access_key: "${TEST_S3_KEY}", bucket: b, prefix: "x/"}
  - name: ev
    type: kafka
    config: {brokers: "h:9092", topic: t, max: "50"}
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Names(); len(got) != 3 {
		t.Fatalf("names = %v", got)
	}
	// env expansion happened
	if m.Spec("lake").Config["access_key"] != "secret-from-env" {
		t.Fatalf("env not expanded: %q", m.Spec("lake").Config["access_key"])
	}
	// each builds to the right concrete type
	mustType := func(name string, want any) {
		s, err := m.BuildByName(name)
		if err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
		switch want.(type) {
		case *MySQLSource:
			if _, ok := s.(*MySQLSource); !ok {
				t.Fatalf("%s: want *MySQLSource, got %T", name, s)
			}
		case *S3Source:
			if _, ok := s.(*S3Source); !ok {
				t.Fatalf("%s: want *S3Source, got %T", name, s)
			}
		case *KafkaSource:
			if _, ok := s.(*KafkaSource); !ok {
				t.Fatalf("%s: want *KafkaSource, got %T", name, s)
			}
		}
	}
	mustType("orders", (*MySQLSource)(nil))
	mustType("lake", (*S3Source)(nil))
	mustType("ev", (*KafkaSource)(nil))

	if _, err := Build(SourceSpec{Name: "x", Type: "nope"}); err == nil {
		t.Fatal("unknown type must error")
	}
}
