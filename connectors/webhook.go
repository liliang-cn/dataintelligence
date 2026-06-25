package connectors

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/liliang-cn/dataintelligence/warehouse"
)

// WebhookServer is a push source: it receives change events that an external
// system (e.g. Twenty CRM) POSTs in real time, the inverse of the poll-based CDC
// source. Each delivery is signature-checked (when a secret is configured),
// recorded to _crm_events, and acknowledged. This is the right edge of "real
// integration": the CRM pushes; we don't poll.
type WebhookServer struct {
	WH      *warehouse.Warehouse
	Secret  string // shared secret for HMAC verification (empty = accept unsigned, log it)
	OnEvent func(WebhookEvent)
}

// WebhookEvent is one received delivery.
type WebhookEvent struct {
	Event    string // e.g. person.created | person.updated | person.deleted
	Object   string // nameSingular, e.g. "person"
	RecordID string
	Verified string // yes | no | unsigned
	Payload  map[string]any
}

// Handler returns the HTTP handler. POST <any path> ingests a delivery.
func (s *WebhookServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /webhook", s.receive)
	return mux
}

func (s *WebhookServer) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	verified := s.verify(r, body)
	if verified == "no" {
		// A bad signature is a security event — reject, don't store.
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	var p struct {
		EventName      string `json:"eventName"`
		ObjectMetadata struct {
			NameSingular string `json:"nameSingular"`
		} `json:"objectMetadata"`
		Record map[string]any `json:"record"`
	}
	_ = json.Unmarshal(body, &p)
	ev := WebhookEvent{
		Event: p.EventName, Object: p.ObjectMetadata.NameSingular, Verified: verified,
	}
	if p.Record != nil {
		ev.RecordID = fmt.Sprintf("%v", p.Record["id"])
		ev.Payload = p.Record
	}
	if err := s.store(r.Context(), ev, body); err != nil {
		http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.OnEvent != nil {
		s.OnEvent(ev)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// verify checks the HMAC-SHA256 of the raw body against the configured secret,
// trying the header names Twenty has used. Returns yes | no | unsigned.
func (s *WebhookServer) verify(r *http.Request, body []byte) string {
	if s.Secret == "" {
		return "unsigned"
	}
	sig := r.Header.Get("X-Twenty-Webhook-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Hub-Signature-256")
	}
	if sig == "" {
		return "unsigned"
	}
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	// accept either bare hex or "sha256=<hex>"
	if sig == want || sig == "sha256="+want {
		return "yes"
	}
	return "no"
}

func (s *WebhookServer) store(ctx context.Context, ev WebhookEvent, raw []byte) error {
	if _, err := s.WH.Exec(ctx, `CREATE TABLE IF NOT EXISTS _crm_events (
		id bigserial PRIMARY KEY, event text, object text, record_id text,
		verified text, payload jsonb, received_at timestamptz DEFAULT now())`); err != nil {
		return err
	}
	_, err := s.WH.Exec(ctx,
		`INSERT INTO _crm_events (event,object,record_id,verified,payload) VALUES ($1,$2,$3,$4,$5)`,
		ev.Event, ev.Object, ev.RecordID, ev.Verified, string(raw))
	return err
}

// WaitForEvent polls _crm_events for a row newer than sinceID (for verification).
func WaitForEvent(ctx context.Context, wh *warehouse.Warehouse, sinceID int64, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := wh.Query(ctx, `SELECT count(*) FROM _crm_events WHERE id > $1`, sinceID)
		if err == nil && len(res.Rows) > 0 {
			if fmt.Sprintf("%v", res.Rows[0][0]) != "0" {
				return true, nil
			}
		}
		time.Sleep(time.Second)
	}
	return false, nil
}
