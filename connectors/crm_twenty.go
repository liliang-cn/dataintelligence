package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TwentyCRM is a real CRM Source backed by a running Twenty instance
// (https://twenty.com) over its REST API. It pulls People (contacts), paginated,
// and supports incremental sync by the `updatedAt` cursor — the same shape as
// the Postgres CDC source, so a CRM behaves like any other governed source.
type TwentyCRM struct {
	BaseURL string // e.g. http://localhost:34100
	APIKey  string
	Object  string // rest object, default "people"
	Client  *http.Client
}

func (c *TwentyCRM) object() string {
	if c.Object == "" {
		return "people"
	}
	return c.Object
}

func (c *TwentyCRM) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *TwentyCRM) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("twenty %s: HTTP %d", path, resp.StatusCode)
	}
	var out map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// flatPerson projects a Twenty person object into our flat Record. The CRM's
// nested name/emails are flattened to the columns we sync into the warehouse —
// crucially `email`, which is the join key back to the Meridian customers.
func flatPerson(p map[string]any) Record {
	r := Record{}
	r["crm_id"] = str(p["id"])
	if n, ok := p["name"].(map[string]any); ok {
		r["first_name"] = str(n["firstName"])
		r["last_name"] = str(n["lastName"])
		r["name"] = strings.TrimSpace(str(n["firstName"]) + " " + str(n["lastName"]))
	}
	if e, ok := p["emails"].(map[string]any); ok {
		r["email"] = str(e["primaryEmail"])
	}
	r["job_title"] = str(p["jobTitle"])
	r["city"] = str(p["city"])
	r["updated_at"] = str(p["updatedAt"])
	return r
}

// Discover returns the synced field set.
func (c *TwentyCRM) Discover(_ context.Context) (SourceSchema, error) {
	return SourceSchema{
		Name: "crm_" + c.object(),
		Fields: []Field{
			{Name: "crm_id", Type: "text"}, {Name: "name", Type: "text"},
			{Name: "first_name", Type: "text"}, {Name: "last_name", Type: "text"},
			{Name: "email", Type: "text"}, {Name: "job_title", Type: "text"},
			{Name: "city", Type: "text"}, {Name: "updated_at", Type: "text"},
		},
	}, nil
}

// Read pulls all People, following cursor pagination.
func (c *TwentyCRM) Read(ctx context.Context) (Batch, error) {
	schema, _ := c.Discover(ctx)
	batch := Batch{Schema: schema}
	rows, err := c.fetchAll(ctx, "")
	if err != nil {
		return Batch{}, err
	}
	batch.Rows = rows
	return batch, nil
}

// fetchAll walks Twenty's cursor pagination (startingAfter) until drained.
func (c *TwentyCRM) fetchAll(ctx context.Context, sinceISO string) ([]Record, error) {
	var out []Record
	after := ""
	for {
		path := fmt.Sprintf("/rest/%s?limit=60", c.object())
		if after != "" {
			path += "&starting_after=" + url.QueryEscape(after)
		}
		doc, err := c.get(ctx, path)
		if err != nil {
			return nil, err
		}
		data, _ := doc["data"].(map[string]any)
		list, _ := data[c.object()].([]any)
		if len(list) == 0 {
			break
		}
		for _, it := range list {
			p, ok := it.(map[string]any)
			if !ok {
				continue
			}
			rec := flatPerson(p)
			if sinceISO != "" && rec["updated_at"] != "" && rec["updated_at"] <= sinceISO {
				continue // incremental: skip rows not changed since the cursor
			}
			out = append(out, rec)
		}
		// pageInfo.endCursor drives the next page.
		pi, _ := doc["pageInfo"].(map[string]any)
		hasNext, _ := pi["hasNextPage"].(bool)
		end := str(pi["endCursor"])
		if !hasNext || end == "" {
			break
		}
		after = end
	}
	return out, nil
}

// Poll returns People updated since the ISO8601 cursor (incremental sync).
func (c *TwentyCRM) Poll(ctx context.Context, sinceISO string) ([]Record, error) {
	return c.fetchAll(ctx, sinceISO)
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
