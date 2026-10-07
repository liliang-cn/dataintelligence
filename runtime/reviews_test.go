package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/dataintelligence/config"
)

// A configured review opens its store, lists, and records a run's row.
func TestReviewsOpenAndRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "di.yaml")
	if err := os.WriteFile(path, []byte("model: m.yaml\nwarehouse: {dsn: \"postgres://x@localhost/x\"}\ncopilot:\n  reviews:\n    - {name: group, title: 集团经营诊断, prompt: 看一下, weekday: mon, hour: 8, roles: [approver]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReviews(cfg)
	if err != nil || r == nil || r.DB == nil {
		t.Fatalf("open: %v %v", r, err)
	}
	if _, err := r.DB.ExecContext(context.Background(), `INSERT INTO runs (name, title, by, trigger, started_at, status) VALUES ('group','集团经营诊断','厂长','manual','2026-10-07T00:00:00Z','done')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.DB.QueryRow(`SELECT count(*) FROM runs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("runs = %d, %v", n, err)
	}
	if _, ok := r.find("group"); !ok {
		t.Fatal("review not found")
	}

	// a run cut off by a restart is closed as failed when the store is opened again
	if _, err := r.DB.Exec(`INSERT INTO runs (name, title, by, trigger, started_at, status) VALUES ('group','集团经营诊断','厂长','manual','2026-10-07T01:00:00Z','running')`); err != nil {
		t.Fatal(err)
	}
	r.DB.Close()
	r, err = OpenReviews(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var running, failed int
	r.DB.QueryRow(`SELECT count(*) FILTER (WHERE status='running'), count(*) FILTER (WHERE status='failed' AND finished_at IS NOT NULL) FROM runs`).Scan(&running, &failed)
	if running != 0 || failed != 1 {
		t.Fatalf("after reopen: running %d, failed %d", running, failed)
	}
}
