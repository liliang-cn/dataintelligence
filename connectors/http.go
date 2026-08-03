package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTPSource reads JSON records from any HTTP API.
//
// It exists because the systems a manufacturer actually runs on — SAP via
// OData, 管家婆云 and 用友/金蝶's cloud editions via their OpenAPI — are all the
// same shape: authenticate, GET a page of JSON, find the array in it, follow
// something to the next page, stop. Only the four details differ, so those four
// are configuration and there is no vendor in this file. A customer's ERP is a
// manifest entry, never a Go file — that is the rule the whole platform is
// built on, and an `sap.go` would be the first crack in it.
//
// This is a *landing* source, not a query path. The semantic layer compiles SQL
// against a warehouse: an API cannot take a pushed-down GROUP BY, cannot be
// cost-estimated with EXPLAIN, and has no role to SET. So records come here,
// land in the warehouse through ingest, and every governed read after that is
// unchanged.
type HTTPSource struct {
	Name   string
	URL    string
	Method string            // GET (default) | POST
	Header map[string]string // extra headers, e.g. sap-client: 100

	// Auth is none | basic | bearer | header | oauth2. oauth2 is
	// client-credentials, which is what both SAP BTP and 管家婆云 issue.
	Auth         string
	Username     string
	Password     string
	Token        string
	HeaderName   string // for auth: header
	TokenURL     string // for auth: oauth2
	ClientID     string
	ClientSecret string
	Scope        string

	// RecordsPath is the dotted path to the array of records: "d.results" for
	// SAP OData v2, "value" for v4, "data.items" for most Chinese OpenAPIs.
	// Empty means the response body is itself the array.
	RecordsPath string

	// Page is none | odata | offset | page | cursor.
	Page       string
	PageSize   int
	PageParam  string // offset/page: the parameter carrying position. Defaults per mode.
	SizeParam  string // offset/page: the parameter carrying size. Defaults per mode.
	NextPath   string // cursor: dotted path to the next-page token in the response
	NextParam  string // cursor: query parameter that carries it back
	MaxPages   int
	MaxRecords int

	// CursorField is the record field holding a last-changed timestamp, used to
	// pull incrementally. Filtering happens client-side here: a server-side
	// filter is better and every API spells it differently, so the manifest puts
	// it in `url` where it belongs.
	CursorField string

	// RPS caps requests per second. It defaults to something slow on purpose:
	// this connector points at a production ERP that the customer's whole
	// business is typing into, and a sync that saturates it is an outage the
	// engagement caused.
	RPS float64

	Client *http.Client

	// Truncated records what was left behind when a cap bit, so the caller can
	// say so instead of presenting a partial pull as a complete one.
	Truncated string

	token   string
	tokenAt time.Time
	last    time.Time
}

const (
	defaultRPS      = 4
	defaultPageSize = 500
	defaultMaxPages = 1000
)

func (h *HTTPSource) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (h *HTTPSource) name() string {
	if h.Name != "" {
		return h.Name
	}
	return "http"
}

// Discover reads one page and reports the fields found in it.
//
// The field set is the union of keys across every record on the page, not the
// keys of the first one. JSON omits null fields rather than emitting them, so
// the first record of an ERP export routinely lacks half the columns — taking
// it as the schema silently drops them, and the model is then built without
// columns the data has.
func (h *HTTPSource) Discover(ctx context.Context) (SourceSchema, error) {
	recs, _, err := h.page(ctx, h.URL)
	if err != nil {
		return SourceSchema{}, err
	}
	return SourceSchema{Name: h.name(), Fields: fieldsOf(recs)}, nil
}

// Read pulls every page, following whichever pagination the manifest declared.
func (h *HTTPSource) Read(ctx context.Context) (Batch, error) {
	rows, err := h.fetchAll(ctx, "")
	if err != nil {
		return Batch{}, err
	}
	return Batch{Schema: SourceSchema{Name: h.name(), Fields: fieldsOf(rows)}, Rows: rows}, nil
}

// Poll returns records whose cursor field is newer than since — the same
// incremental contract as the CDC and CRM sources, so a REST ERP schedules like
// any other source.
func (h *HTTPSource) Poll(ctx context.Context, since string) ([]Record, error) {
	return h.fetchAll(ctx, since)
}

func (h *HTTPSource) fetchAll(ctx context.Context, since string) ([]Record, error) {
	h.Truncated = ""
	maxPages := h.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}

	var out []Record
	next := h.URL
	seen := map[string]bool{}
	for page := 0; next != "" && page < maxPages; page++ {
		// A pagination bug that returns the same next-link forever is a loop
		// against someone else's production system. Stopping is not optional.
		if seen[next] {
			h.Truncated = "pagination repeated a page it had already fetched; stopped"
			break
		}
		seen[next] = true

		recs, more, err := h.page(ctx, next)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if since != "" && h.CursorField != "" && r[h.CursorField] != "" && r[h.CursorField] <= since {
				continue
			}
			out = append(out, r)
		}
		if h.MaxRecords > 0 && len(out) >= h.MaxRecords {
			out = out[:h.MaxRecords]
			h.Truncated = fmt.Sprintf("stopped at max_records=%d; the source had more", h.MaxRecords)
			break
		}
		if len(recs) == 0 {
			break
		}
		next = h.nextURL(next, more, page+1, len(out))
	}
	if h.Truncated == "" && len(seen) >= maxPages {
		h.Truncated = fmt.Sprintf("stopped at max_pages=%d; the source had more", maxPages)
	}
	return out, nil
}

// page fetches one URL and returns its records plus the raw document, which
// pagination needs for next-links and tokens.
func (h *HTTPSource) page(ctx context.Context, u string) ([]Record, map[string]any, error) {
	body, err := h.do(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: response is not JSON: %w", h.name(), err)
	}
	arr, err := arrayAt(doc, h.RecordsPath)
	if err != nil {
		return nil, nil, err
	}
	recs := make([]Record, 0, len(arr))
	for _, it := range arr {
		recs = append(recs, flatten(it))
	}
	m, _ := doc.(map[string]any)
	return recs, m, nil
}

// nextURL computes the following page's URL for the configured strategy.
func (h *HTTPSource) nextURL(current string, doc map[string]any, pageNo, got int) string {
	size := h.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	switch strings.ToLower(h.Page) {
	case "", "none":
		return ""

	case "odata":
		// SAP emits the next link as an absolute URL, and behind a reverse
		// proxy or Web Dispatcher it is the *internal* hostname — a host the
		// client cannot resolve, or worse, can. Only the path and query are
		// trustworthy; the scheme and host stay the ones we were configured
		// with and are allowed to reach.
		link := str(doc["@odata.nextLink"])
		if link == "" {
			if d, ok := doc["d"].(map[string]any); ok {
				link = str(d["__next"])
			}
		}
		if link == "" {
			return ""
		}
		return rebase(current, link)

	case "offset":
		return withParams(current, map[string]string{
			orDefault(h.PageParam, "$skip"): strconv.Itoa(got),
			orDefault(h.SizeParam, "$top"):  strconv.Itoa(size),
		})

	case "page":
		return withParams(current, map[string]string{
			orDefault(h.PageParam, "page"):      strconv.Itoa(pageNo + 1),
			orDefault(h.SizeParam, "page_size"): strconv.Itoa(size),
		})

	case "cursor":
		next, _ := valueAt(doc, h.NextPath)
		tok := str(next)
		if tok == "" {
			return ""
		}
		return withParams(current, map[string]string{orDefault(h.NextParam, "cursor"): tok})

	default:
		return ""
	}
}

// do issues one request, under the rate limit, retrying what is worth retrying.
func (h *HTTPSource) do(ctx context.Context, u string) ([]byte, error) {
	const attempts = 4
	var lastErr error
	for i := 0; i < attempts; i++ {
		h.throttle(ctx)

		req, err := http.NewRequestWithContext(ctx, orDefault(h.Method, http.MethodGet), u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range h.Header {
			req.Header.Set(k, v)
		}
		if err := h.authorize(ctx, req); err != nil {
			return nil, err
		}

		resp, err := h.client().Do(req)
		if err != nil {
			lastErr = err
			sleep(ctx, backoff(i))
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			sleep(ctx, backoff(i))
			continue
		}

		switch {
		case resp.StatusCode < 300:
			return body, nil
		// 429 and 5xx are the server saying "later", and an ERP under month-end
		// close says it a lot. Giving up on the first one turns a slow sync into
		// a failed delivery; retrying without honouring Retry-After turns it
		// into the reason the customer's ERP fell over.
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d: %s", h.name(), resp.StatusCode, snippet(body))
			sleep(ctx, retryAfter(resp, backoff(i)))
		case resp.StatusCode == http.StatusUnauthorized && h.tokenExpired():
			h.token = "" // a token that aged out mid-pull: re-mint once and retry
			lastErr = fmt.Errorf("%s: HTTP 401", h.name())
		default:
			// 4xx is a configuration error — a wrong path, a missing sap-client,
			// an unentitled service. Retrying cannot fix it and hides it.
			return nil, fmt.Errorf("%s: HTTP %d: %s", h.name(), resp.StatusCode, snippet(body))
		}
	}
	return nil, lastErr
}

// throttle spaces requests out. The ceiling is deliberately low by default.
func (h *HTTPSource) throttle(ctx context.Context) {
	rps := h.RPS
	if rps <= 0 {
		rps = defaultRPS
	}
	gap := time.Duration(float64(time.Second) / rps)
	if wait := gap - time.Since(h.last); wait > 0 && !h.last.IsZero() {
		sleep(ctx, wait)
	}
	h.last = time.Now()
}

func (h *HTTPSource) authorize(ctx context.Context, req *http.Request) error {
	switch strings.ToLower(h.Auth) {
	case "", "none":
		return nil
	case "basic":
		req.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte(h.Username+":"+h.Password)))
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+h.Token)
	case "header":
		req.Header.Set(orDefault(h.HeaderName, "X-API-Key"), h.Token)
	case "oauth2":
		tok, err := h.oauth2(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	default:
		return fmt.Errorf("%s: unknown auth %q", h.name(), h.Auth)
	}
	return nil
}

func (h *HTTPSource) tokenExpired() bool {
	return strings.EqualFold(h.Auth, "oauth2") && h.token != ""
}

// oauth2 mints a client-credentials token and caches it.
func (h *HTTPSource) oauth2(ctx context.Context) (string, error) {
	// Re-mint a minute early. A token that expires between the request being
	// built and the server reading it fails a page in the middle of a pull,
	// which is the least debuggable moment for it to happen.
	if h.token != "" && time.Since(h.tokenAt) < 50*time.Minute {
		return h.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if h.Scope != "" {
		form.Set("scope", h.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(h.ClientID, h.ClientSecret)
	resp, err := h.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: token endpoint HTTP %d: %s", h.name(), resp.StatusCode, snippet(body))
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", fmt.Errorf("%s: token endpoint returned no access_token", h.name())
	}
	h.token, h.tokenAt = t.AccessToken, time.Now()
	return h.token, nil
}

// --- JSON shaping ---

// arrayAt walks the dotted path to the record array.
//
// A path whose keys are all present but whose value is null is the last page of
// a source that spells "no more" that way — common, and not an error. A path
// with a *missing* key is a typo, and it is the commonest way this connector is
// misconfigured: reported as an empty pull it looks exactly like a quiet source,
// and the delivery lands with a table that never fills.
func arrayAt(doc any, path string) ([]any, error) {
	v, found := valueAt(doc, path)
	if !found {
		return nil, fmt.Errorf("records_path %q found nothing in the response", path)
	}
	if v == nil {
		return nil, nil // present and empty: the source is drained
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("records_path %q is a %T, not an array", path, v)
	}
	return arr, nil
}

// valueAt walks a dotted path, reporting whether every segment existed.
func valueAt(doc any, path string) (any, bool) {
	cur := doc
	if path == "" {
		return cur, true
	}
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[part]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// flatten turns one nested JSON object into the flat Record the warehouse takes.
//
// Nested objects become underscore-joined columns. Arrays are kept as JSON text
// rather than dropped: an ERP line-item array is the whole point of the row it
// hangs off, and silently losing it produces a table that looks complete.
func flatten(v any) Record {
	r := Record{}
	flattenInto(r, "", v)
	return r
}

func flattenInto(r Record, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			key := k
			if prefix != "" {
				key = prefix + "_" + k
			}
			// SAP OData v2 wraps every entity in a "__metadata" object of URIs
			// and etags. It is transport, not data.
			if strings.HasPrefix(k, "__") {
				continue
			}
			flattenInto(r, key, sub)
		}
	case []any:
		b, _ := json.Marshal(t)
		r[prefix] = string(b)
	case nil:
		if prefix != "" {
			r[prefix] = ""
		}
	default:
		r[prefix] = str(t)
	}
}

// fieldsOf unions the keys of every record and infers each column's type with
// the same rule every other source uses.
func fieldsOf(recs []Record) []Field {
	values := map[string][]string{}
	for _, r := range recs {
		for k, v := range r {
			values[k] = append(values[k], v)
		}
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names) // stable across runs, so a schema diff is a real change
	out := make([]Field, 0, len(names))
	for _, n := range names {
		out = append(out, Field{Name: n, Type: inferType(values[n])})
	}
	return out
}

// --- small helpers ---

// rebase keeps the host we were configured to talk to and takes only the path
// and query from a server-supplied link.
func rebase(current, link string) string {
	base, err := url.Parse(current)
	if err != nil {
		return link
	}
	next, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if !next.IsAbs() {
		return base.ResolveReference(next).String()
	}
	next.Scheme, next.Host = base.Scheme, base.Host
	return next.String()
}

func withParams(u string, params map[string]string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	q := parsed.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt)) * time.Second
}

func retryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.Join(strings.Fields(s), " ")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
