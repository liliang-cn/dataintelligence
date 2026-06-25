package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ESSink delivers rows to Elasticsearch as a _bulk index request. With URL set
// it POSTs to <URL>/<index>/_bulk; with URL empty it runs in dry-run mode and
// reports the payload it WOULD send (simulate ES without a cluster).
type ESSink struct {
	URL   string
	Index string
}

func (s ESSink) Name() string { return "elasticsearch" }
func (s ESSink) Write(ctx context.Context, cols []string, rows [][]any) (WriteResult, error) {
	var ndjson strings.Builder
	for _, r := range rows {
		meta, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": s.Index}})
		doc, _ := json.Marshal(toMap(cols, r))
		ndjson.Write(meta)
		ndjson.WriteByte('\n')
		ndjson.Write(doc)
		ndjson.WriteByte('\n')
	}
	if s.URL == "" {
		note := fmt.Sprintf("DRY-RUN: would _bulk index %d docs into %q", len(rows), s.Index)
		return WriteResult{Sink: "elasticsearch", Count: len(rows), Target: s.Index, Note: note}, nil
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	url := strings.TrimRight(s.URL, "/") + "/" + s.Index + "/_bulk?refresh=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(ndjson.String()))
	if err != nil {
		return WriteResult{}, err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := cl.Do(req)
	if err != nil {
		return WriteResult{}, err
	}
	defer resp.Body.Close()
	return WriteResult{Sink: "elasticsearch", Count: len(rows), Target: s.Index, Note: resp.Status}, nil
}

// SnowflakeSink simulates loading rows into a Snowflake table. Without a driver
// it runs dry-run and reports the SQL it WOULD execute (CREATE + multi-row
// INSERT). Swap in gosnowflake to make it live.
type SnowflakeSink struct {
	Table string
}

func (s SnowflakeSink) Name() string { return "snowflake" }
func (s SnowflakeSink) Write(_ context.Context, cols []string, rows [][]any) (WriteResult, error) {
	var defs []string
	for _, c := range cols {
		defs = append(defs, fmt.Sprintf("%s STRING", c))
	}
	sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s);\nINSERT INTO %s (%s) VALUES %s rows...;",
		s.Table, join(defs), s.Table, join(cols), fmt.Sprintf("(%d rows)", len(rows)))
	note := fmt.Sprintf("DRY-RUN: would load %d rows into Snowflake %q\n%s", len(rows), s.Table, sql)
	return WriteResult{Sink: "snowflake", Count: len(rows), Target: s.Table, Note: note}, nil
}
