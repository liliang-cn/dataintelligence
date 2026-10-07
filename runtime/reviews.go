package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/liliang-cn/dataintelligence/config"
	"github.com/liliang-cn/dataintelligence/copilot"
	"github.com/liliang-cn/dataintelligence/governance"
)

// Reviews are standing consultations: the same question put to the copilot every week (a
// whole-business review, say) or whenever someone allowed starts one. Every run is kept with its
// answer and its steps, so a report can be read afterwards and compared with the last one.
//
//	GET  /v1/reviews               the reviews, whether the caller may start each, recent runs
//	GET  /v1/reviews/runs/{id}     one run with its steps
//	POST /v1/reviews/{name}/run    start one now; text/event-stream like /v1/copilot/stream
type Reviews struct {
	List    []config.Review
	Prompts map[string]string
	DB      *sql.DB
	Engage  string
	running sync.Map
}

// OpenReviews loads the prompts and opens the report store.
func OpenReviews(cfg *config.Config) (*Reviews, error) {
	if len(cfg.Copilot.Reviews) == 0 {
		return nil, nil
	}
	r := &Reviews{List: cfg.Copilot.Reviews, Prompts: map[string]string{}, Engage: cfg.Engagement}
	for _, rv := range r.List {
		p := rv.Prompt
		if rv.PromptFile != "" {
			b, err := os.ReadFile(cfg.Path(rv.PromptFile))
			if err != nil {
				return nil, fmt.Errorf("copilot.reviews %s: %w", rv.Name, err)
			}
			p = string(b)
		}
		if strings.TrimSpace(p) == "" || rv.Name == "" {
			return nil, fmt.Errorf("copilot.reviews: each review needs a name and a prompt")
		}
		r.Prompts[rv.Name] = p
	}
	path := cfg.Copilot.ReviewStore
	if path == "" {
		path = "reviews.db"
	}
	db, err := sql.Open("sqlite", cfg.Path(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, title TEXT NOT NULL, by TEXT NOT NULL, trigger TEXT NOT NULL,
		started_at TEXT NOT NULL, finished_at TEXT, answer TEXT, steps TEXT, corrected TEXT, status TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reviews) find(name string) (config.Review, bool) {
	for _, rv := range r.List {
		if rv.Name == name {
			return rv, true
		}
	}
	return config.Review{}, false
}

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
var weekdayZh = map[string]string{"sun": "周日", "mon": "周一", "tue": "周二", "wed": "周三", "thu": "周四", "fri": "周五", "sat": "周六"}

func zone(rv config.Review) *time.Location {
	name := rv.Timezone
	if name == "" {
		name = "Asia/Shanghai"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

type step struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
	Result any            `json:"result,omitempty"`
}

// run asks the copilot one review's question as p, recording the run; emit (may be nil) sees the stream.
func (r *Reviews) run(ctx context.Context, cop *copilot.Agent, rv config.Review, p governance.Principal, trigger string, emit func(copilot.StreamEvent)) error {
	if _, busy := r.running.LoadOrStore(rv.Name, true); busy {
		return fmt.Errorf("「%s」正在进行", rv.Title)
	}
	defer r.running.Delete(rv.Name)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.DB.ExecContext(ctx, `INSERT INTO runs (name, title, by, trigger, started_at, status) VALUES (?,?,?,?,?,?)`, rv.Name, rv.Title, p.User, trigger, now, "running")
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	var steps []step
	var corrected, answer string
	collect := func(ev copilot.StreamEvent) {
		switch ev.Kind {
		case "tool_call":
			steps = append(steps, step{Tool: ev.Tool, Args: ev.Args})
		case "tool_result":
			for i := len(steps) - 1; i >= 0; i-- {
				if steps[i].Tool == ev.Tool && steps[i].Result == nil {
					steps[i].Result = ev.Result
					break
				}
			}
		case "verified", "verify_skipped":
			corrected = ev.Text
		case "complete":
			answer = ev.Text
		}
		if emit != nil {
			emit(ev)
		}
	}
	if p.User != "" {
		ctx = copilot.WithPrincipal(ctx, p)
	}
	_, rerr := cop.Stream(ctx, r.Prompts[rv.Name], collect)
	status := "done"
	if rerr != nil || strings.HasPrefix(answer, "error:") {
		status = "failed"
		if rerr != nil && answer == "" {
			answer = rerr.Error()
		}
	}
	sb, _ := json.Marshal(steps)
	// the run outlives a viewer who closed the page: record it on a fresh context
	_, err = r.DB.ExecContext(context.Background(), `UPDATE runs SET finished_at=?, answer=?, steps=?, corrected=?, status=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), answer, string(sb), corrected, status, id)
	if rerr != nil {
		return rerr
	}
	return err
}

// Schedule runs each weekly review once in its hour.
func (r *Reviews) Schedule(ctx context.Context, cop *copilot.Agent, log func(string, ...any)) {
	if r == nil || cop == nil {
		return
	}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			for _, rv := range r.List {
				wd, ok := weekdays[strings.ToLower(rv.Weekday)]
				if !ok {
					continue
				}
				now := time.Now().In(zone(rv))
				if now.Weekday() != wd || now.Hour() != rv.Hour {
					continue
				}
				day := now.Format("2006-01-02")
				var n int
				_ = r.DB.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE name=? AND trigger='schedule' AND substr(started_at,1,10) >= ?`,
					rv.Name, now.AddDate(0, 0, -1).UTC().Format("2006-01-02")).Scan(&n)
				if n > 0 {
					continue
				}
				p := governance.Principal{User: rv.As.User, Role: rv.As.Role, Attrs: rv.As.Attrs, Engagement: r.Engage}
				if p.User == "" {
					p.User = "定期诊断"
				}
				if p.Role == "" {
					p.Role = "analyst"
				}
				log("review started", "name", rv.Name, "day", day)
				go func(rv config.Review) {
					if err := r.run(ctx, cop, rv, p, "schedule", nil); err != nil {
						log("review failed", "name", rv.Name, "err", err)
					}
				}(rv)
			}
		}
	}()
}

func (v *V1) mountReviews(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/reviews", v.reviewList)
	mux.HandleFunc("GET /v1/reviews/runs/{id}", v.reviewRun)
	mux.HandleFunc("POST /v1/reviews/{name}/run", v.reviewStart)
}

func (v *V1) reviewList(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeErr(w, 401, errString(errText(err)))
		return
	}
	if v.Reviews == nil {
		writeJSON(w, 200, map[string]any{"reviews": []any{}, "runs": []any{}})
		return
	}
	type item struct {
		Name     string `json:"name"`
		Title    string `json:"title"`
		Schedule string `json:"schedule,omitempty"`
		CanRun   bool   `json:"can_run"`
		Running  bool   `json:"running"`
	}
	items := []item{}
	for _, rv := range v.Reviews.List {
		sch := ""
		if zh, ok := weekdayZh[strings.ToLower(rv.Weekday)]; ok {
			sch = fmt.Sprintf("每%s %02d:00", zh, rv.Hour)
		}
		_, running := v.Reviews.running.Load(rv.Name)
		items = append(items, item{rv.Name, rv.Title, sch, slices.Contains(rv.Roles, p.Role), running})
	}
	rows, err := v.Reviews.DB.QueryContext(r.Context(), `SELECT id, name, title, by, trigger, started_at, coalesce(finished_at,''), coalesce(answer,''), status FROM runs ORDER BY id DESC LIMIT 30`)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, title, by, trigger, started, finished, answer, status string
		if rows.Scan(&id, &name, &title, &by, &trigger, &started, &finished, &answer, &status) == nil {
			runs = append(runs, map[string]any{"id": id, "name": name, "title": title, "by": by, "trigger": trigger,
				"started_at": started, "finished_at": finished, "answer": answer, "status": status})
		}
	}
	writeJSON(w, 200, map[string]any{"reviews": items, "runs": runs})
}

func (v *V1) reviewRun(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := v.principalFrom(r); !ok {
		writeErr(w, 401, errString(errText(err)))
		return
	}
	if v.Reviews == nil {
		writeErr(w, 404, errString("没有定期诊断"))
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var name, title, by, trigger, started, finished, answer, steps, corrected, status string
	err := v.Reviews.DB.QueryRowContext(r.Context(), `SELECT name, title, by, trigger, started_at, coalesce(finished_at,''), coalesce(answer,''), coalesce(steps,'[]'), coalesce(corrected,''), status FROM runs WHERE id=?`, id).
		Scan(&name, &title, &by, &trigger, &started, &finished, &answer, &steps, &corrected, &status)
	if err != nil {
		writeErr(w, 404, errString("没有这次诊断"))
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "name": name, "title": title, "by": by, "trigger": trigger, "started_at": started,
		"finished_at": finished, "answer": answer, "steps": json.RawMessage(steps), "corrected": corrected, "status": status})
}

func (v *V1) reviewStart(w http.ResponseWriter, r *http.Request) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		writeErr(w, 401, errString(errText(err)))
		return
	}
	if v.Reviews == nil || v.Copilot == nil {
		writeErr(w, 404, errString("没有定期诊断"))
		return
	}
	rv, found := v.Reviews.find(r.PathValue("name"))
	if !found {
		writeErr(w, 404, errString("没有这个诊断"))
		return
	}
	if !slices.Contains(rv.Roles, p.Role) {
		writeErr(w, 403, errString("没有发起「"+rv.Title+"」的权限"))
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeErr(w, 500, errString("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	var mu sync.Mutex
	gone := false
	send := func(ev copilot.StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		if gone {
			return
		}
		b, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			gone = true
			return
		}
		flusher.Flush()
	}
	// the review keeps going if the viewer leaves: it is recorded either way
	done := make(chan error, 1)
	go func() { done <- v.Reviews.run(context.WithoutCancel(r.Context()), v.Copilot, rv, p, "manual", send) }()
	select {
	case err := <-done:
		if err != nil {
			send(copilot.StreamEvent{Kind: "error", Text: err.Error()})
		}
	case <-r.Context().Done():
		mu.Lock()
		gone = true
		mu.Unlock()
	}
}
