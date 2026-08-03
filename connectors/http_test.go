package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fast returns a source configured not to sleep through the test suite. The
// rate limit is the point of the default, not of these tests.
func fast(h *HTTPSource) *HTTPSource {
	h.RPS = 10000
	return h
}

// SAP OData v2 nests the array under d.results and the next page under d.__next.
func TestODataV2Pagination(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skip, _ := strconv.Atoi(r.URL.Query().Get("$skip"))
		body := map[string]any{"d": map[string]any{
			"results": []any{
				map[string]any{"Vbeln": fmt.Sprintf("%04d", skip), "Netwr": "100.5",
					"__metadata": map[string]any{"uri": "should-not-appear"}},
			},
		}}
		if skip < 2 {
			body["d"].(map[string]any)["__next"] = srv.URL + "/svc?$skip=" + strconv.Itoa(skip+1)
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	src := fast(&HTTPSource{URL: srv.URL + "/svc", RecordsPath: "d.results", Page: "odata"})
	b, err := src.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 3 {
		t.Fatalf("want 3 rows across 3 pages, got %d", len(b.Rows))
	}
	// __metadata is transport, not data. Landing it makes a column of URIs.
	for k := range b.Rows[0] {
		if strings.Contains(k, "metadata") || strings.Contains(k, "uri") {
			t.Errorf("SAP transport wrapper leaked into the record: %s", k)
		}
	}
}

// SAP behind a Web Dispatcher hands back a next-link naming an internal host.
// Following it verbatim either fails to resolve or reaches a machine we were
// never pointed at.
func TestODataNextLinkKeepsTheHostWeWereGiven(t *testing.T) {
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		body := map[string]any{"value": []any{map[string]any{"id": n}}}
		if n == 1 {
			body["@odata.nextLink"] = "http://sapprd-internal.corp:8000/svc?$skip=1"
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	src := fast(&HTTPSource{URL: srv.URL + "/svc", RecordsPath: "value", Page: "odata"})
	b, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("followed the internal hostname instead of rebasing: %v", err)
	}
	if len(b.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(b.Rows))
	}
}

// A next-link that never changes is a loop against a customer's production ERP.
func TestPaginationThatRepeatsItselfStops(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value":           []any{map[string]any{"id": 1}},
			"@odata.nextLink": r.URL.String(),
		})
	}))
	defer srv.Close()

	src := fast(&HTTPSource{URL: srv.URL + "/svc", RecordsPath: "value", Page: "odata"})
	if _, err := src.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("fetched the same page %d times", n)
	}
	if src.Truncated == "" {
		t.Error("stopping early must be reported, not silent")
	}
}

// Offset paging is what most Chinese OpenAPIs and OData $skip/$top both are.
func TestOffsetPagination(t *testing.T) {
	const total = 7
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skip, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var rows []any
		for i := skip; i < min(skip+3, total); i++ {
			rows = append(rows, map[string]any{"id": i})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": rows}})
	}))
	defer srv.Close()

	src := fast(&HTTPSource{
		URL: srv.URL + "/api", RecordsPath: "data.items", Page: "offset",
		PageParam: "offset", SizeParam: "limit", PageSize: 3,
	})
	b, err := src.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != total {
		t.Fatalf("want %d rows, got %d", total, len(b.Rows))
	}
}

// 429 under month-end close is the server saying "later", and Retry-After is it
// saying how much later.
func TestRetriesOnThrottleAndHonoursRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"id": 1}})
	}))
	defer srv.Close()

	src := fast(&HTTPSource{URL: srv.URL})
	b, err := src.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 1 || atomic.LoadInt32(&hits) != 2 {
		t.Errorf("hits=%d rows=%d — want one retry then success", hits, len(b.Rows))
	}
}

// A wrong path or a missing sap-client is a configuration error. Retrying it
// four times cannot fix it and delays the message that would.
func TestClientErrorsAreNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, `{"error":"service not entitled"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := fast(&HTTPSource{URL: srv.URL}).Read(context.Background()); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "not entitled") {
		t.Errorf("error must carry the server's reason, got: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("retried a 403 %d times", n)
	}
}

// records_path pointing at nothing is the commonest misconfiguration, and an
// empty pull reported as success is indistinguishable from a quiet source.
func TestWrongRecordsPathIsAnErrorNotAnEmptyPull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"d": map[string]any{"results": []any{}}})
	}))
	defer srv.Close()

	_, err := fast(&HTTPSource{URL: srv.URL, RecordsPath: "data.items"}).Read(context.Background())
	if err == nil {
		t.Fatal("a records_path that matches nothing must fail loudly")
	}
	if !strings.Contains(err.Error(), "data.items") {
		t.Errorf("error must name the path, got: %v", err)
	}
}

// JSON omits null fields. Taking the first record as the schema drops every
// column that happened to be empty on row one.
func TestSchemaIsTheUnionOfAllRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"vbeln": "1"},
			map[string]any{"vbeln": "2", "waers": "CNY", "netwr": 10.5},
		})
	}))
	defer srv.Close()

	s, err := fast(&HTTPSource{URL: srv.URL}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range s.Fields {
		got[f.Name] = f.Type
	}
	for _, want := range []string{"vbeln", "waers", "netwr"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s — schema was taken from one record, got %v", want, got)
		}
	}
	if got["netwr"] != "numeric" {
		t.Errorf("netwr should be numeric, got %q", got["netwr"])
	}
}

// A line-item array is the reason its header row exists. Dropping it produces a
// table that looks complete.
func TestNestedObjectsFlattenAndArraysSurvive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{
			"header": map[string]any{"vbeln": "0001", "kunnr": "C1"},
			"items":  []any{map[string]any{"posnr": "10"}},
		}})
	}))
	defer srv.Close()

	b, err := fast(&HTTPSource{URL: srv.URL}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := b.Rows[0]
	if r["header_vbeln"] != "0001" {
		t.Errorf("nested object not flattened: %v", r)
	}
	if !strings.Contains(r["items"], "posnr") {
		t.Errorf("line items dropped: %v", r)
	}
}

// Incremental sync is the same contract the CDC and CRM sources offer.
func TestPollSkipsRecordsAtOrBeforeTheCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"id": "a", "changed_at": "2026-01-01T00:00:00Z"},
			map[string]any{"id": "b", "changed_at": "2026-08-01T00:00:00Z"},
		})
	}))
	defer srv.Close()

	rows, err := fast(&HTTPSource{URL: srv.URL, CursorField: "changed_at"}).
		Poll(context.Background(), "2026-06-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["id"] != "b" {
		t.Errorf("want only the row after the cursor, got %v", rows)
	}
}

// The default rate limit is a promise to the customer whose ERP this points at.
func TestRateLimitIsOnByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skip, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var rows []any
		if skip < 2 {
			rows = append(rows, map[string]any{"id": skip})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()

	src := &HTTPSource{URL: srv.URL, Page: "offset", PageParam: "offset", PageSize: 1, RPS: 20}
	start := time.Now()
	if _, err := src.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("three requests at 20 rps took %v — the throttle is not applied", elapsed)
	}
}

// oauth2 client-credentials is what SAP BTP and the Chinese cloud ERPs issue.
func TestOAuth2ClientCredentialsMintsOnceAndReuses(t *testing.T) {
	var mints int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&mints, 1)
			if u, p, _ := r.BasicAuth(); u != "cid" || p != "secret" {
				http.Error(w, "bad client", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		skip, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var rows []any
		if skip < 2 {
			rows = append(rows, map[string]any{"id": skip})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()

	src := fast(&HTTPSource{
		URL: srv.URL + "/api", Auth: "oauth2", TokenURL: srv.URL + "/token",
		ClientID: "cid", ClientSecret: "secret",
		Page: "offset", PageParam: "offset", PageSize: 1,
	})
	b, err := src.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(b.Rows))
	}
	if n := atomic.LoadInt32(&mints); n != 1 {
		t.Errorf("minted a token %d times across 3 requests", n)
	}
}

// The manifest is the only place a vendor may appear.
func TestBuildFromManifest(t *testing.T) {
	s, err := Build(SourceSpec{Name: "sap_billing", Type: "odata", Config: map[string]string{
		"url": "https://sap.example/svc", "auth": "basic", "username": "u", "password": "p",
		"records_path": "d.results", "page": "odata", "rps": "2",
		"header.sap-client": "100", "cursor_field": "LastChangedAt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, ok := s.(*HTTPSource)
	if !ok {
		t.Fatalf("want *HTTPSource, got %T", s)
	}
	if h.Header["sap-client"] != "100" {
		t.Errorf("arbitrary headers must pass through: %v", h.Header)
	}
	if h.RPS != 2 || h.RecordsPath != "d.results" || h.CursorField != "LastChangedAt" {
		t.Errorf("config not wired: %+v", h)
	}
}
