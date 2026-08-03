package survey

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// SegmentGap is one part of a table that stopped while the rest kept going.
//
// A table-level staleness check cannot see this, and in a company with ten
// plants it is the shape the problem actually takes: one site's feed stops, the
// table as a whole still moves because the other nine report, and the totals
// are quietly missing a plant. Nothing errors, and the number that goes to the
// board is short by whatever that site produces.
type SegmentGap struct {
	Table   string `json:"table"`
	Column  string `json:"column"`  // the time column
	By      string `json:"by"`      // the segmenting column
	Segment string `json:"segment"` // the value that stopped
	Newest  string `json:"newest"`
	Behind  int    `json:"periods_behind"` // how far behind the rest of the table
}

// ImpliedRef is a column that references another table by name and by data,
// with no foreign key declared.
//
// Cross-system code matching never has a foreign key: the melt numbers come
// from one system and the pouring records from another, and the column that
// ties them is a convention nobody could enforce. Which is exactly why it
// breaks, and exactly why the declared-key check cannot see it.
type ImpliedRef struct {
	Table     string `json:"table"`
	Column    string `json:"column"`
	RefTable  string `json:"ref_table"`
	RefColumn string `json:"ref_column"`
	Orphans   int64  `json:"orphans"`
	Total     int64  `json:"total"`
}

// findSegmentGaps looks for parts of a table that stopped while the rest did not.
//
// Segments come from the low-cardinality columns already profiled, so this costs
// one query per (time column × segment column) rather than a scan per row.
func findSegmentGaps(ctx context.Context, wh *warehouse.Warehouse, q func(string) string, t Table) []SegmentGap {
	var times, segments []Column
	for _, c := range t.Columns {
		switch {
		case c.Max != "":
			times = append(times, c)
		// A useful segment names something — a plant, a line, a shift. A numeric
		// column with few values is usually a measure that happens to repeat:
		// coke tonnage per workshop looked like a five-way segmentation, and
		// "coke_t = 382.000 stopped reporting" is not a sentence anyone can act on.
		// Never from a sampled count. A sample that drew three plants out of
		// eleven would report the other eight as segments that stopped
		// reporting — eight false alarms in the section the reader most needs
		// to take seriously.
		case c.Distinct >= 2 && c.Distinct <= 24 && !c.Approx && isSegmentLike(c):
			segments = append(segments, c)
		}
	}
	if len(times) == 0 || len(segments) == 0 || t.Rows < minRowsForCadence {
		return nil
	}
	// One query per (time × segment) pair is a full GROUP BY scan each, and the
	// product grows with the square of the table's width: a two-hundred-column
	// fact table with ten date columns and twenty codes is two hundred scans
	// for one table. That is worse than the per-column probing this was meant
	// to complement, and it lands on the customer's production database on day
	// one.
	//
	// One time column is enough. A feed that stopped stops in the table's event
	// time, and a table's event time is the column that runs latest — ship_date
	// and order_date both go quiet together, so checking both buys nothing and
	// costs a scan.
	sort.Slice(times, func(i, j int) bool {
		a, aok := parseTime(times[i].Max)
		b, bok := parseTime(times[j].Max)
		return aok && bok && a.After(b)
	})
	times = times[:1]
	if len(segments) > maxSegmentColumns {
		// Lowest cardinality first: a plant, a line, a shift. Those are what
		// anyone means by "one site stopped reporting"; a code with twenty
		// values is already close to being an identifier.
		sort.Slice(segments, func(i, j int) bool { return segments[i].Distinct < segments[j].Distinct })
		segments = segments[:maxSegmentColumns]
	}

	var out []SegmentGap
	for _, tc := range times {
		overall, ok := parseTime(tc.Max)
		if !ok {
			continue
		}
		for _, sc := range segments {
			rows, err := wh.Query(ctx, fmt.Sprintf(
				"SELECT %s, %s FROM %s GROUP BY %s",
				q(sc.Name), textCast(wh.Driver())("MAX("+q(tc.Name)+")"), q(t.Name), q(sc.Name)))
			if err != nil {
				continue
			}
			// Each segment needs several observations for "behind" to mean
			// anything. A dimension with one row per value — seven production
			// lines, each with the date it was commissioned — segments into
			// seven groups of one, and every group that is not the newest looks
			// like a feed that stopped. It is a list of commissioning dates.
			if len(rows.Rows) == 0 || t.Rows/int64(len(rows.Rows)) < 3 {
				continue
			}
			// The cadence is the table's own: how far apart its periods are.
			// Being "two months behind" only means something once you know the
			// data arrives monthly.
			// Periods come from the time column's own distinct count — twenty-five
			// months — not from the number of segments. Dividing the span by the
			// segment count made a monthly feed look like it reported every four
			// months, and a plant four months silent then read as one period late.
			cadence := CadenceOf(overall, tc.Min, int(tc.Distinct))
			for _, row := range rows.Rows {
				if len(row) < 2 {
					continue
				}
				seg, newest := strings.TrimSpace(cellString(row[0])), strings.TrimSpace(cellString(row[1]))
				last, ok := parseTime(newest)
				if !ok {
					continue
				}
				behind := int(overall.Sub(last) / cadence)
				if behind >= 2 {
					out = append(out, SegmentGap{
						Table: t.Name, Column: tc.Name, By: sc.Name,
						Segment: seg, Newest: last.Format("2006-01-02"), Behind: behind,
					})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Behind > out[j].Behind })
	return out
}

// maxSegmentColumns caps how many ways one table is sliced looking for a part
// that stopped. Six is more than any real "one site went quiet" needs, and it
// keeps a wide table from costing a scan per code column.
const maxSegmentColumns = 6

// isSegmentLike reports whether a column names a thing rather than measures one.
func isSegmentLike(c Column) bool {
	if !isNumericType(c.Type) {
		return true // text, boolean, enum: a name
	}
	n := strings.ToLower(c.Name)
	for _, suffix := range []string{"_id", "_no", "_code", "_key"} {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// cadenceOf estimates how far apart the table's periods are: the span divided
// by how many distinct periods it holds.
//
// A month is the floor. Without one, a daily feed makes "two periods behind"
// fire on an ordinary weekend, and a check that goes off every Monday is a
// check nobody reads by Wednesday.
// CadenceOf is exported because the day-2 drift check must judge staleness the
// same way. A monthly feed is not late for being thirty-three days old, and a
// gate that says so every August is one nobody reads by September.
func CadenceOf(newest time.Time, min string, periods int) time.Duration {
	const floor = 20 * 24 * time.Hour
	oldest, ok := parseTime(min)
	if !ok || periods <= 1 {
		return 30 * 24 * time.Hour
	}
	span := newest.Sub(oldest)
	if span <= 0 {
		return 30 * 24 * time.Hour
	}
	if est := span / time.Duration(periods-1); est > floor {
		return est
	}
	return floor
}

// findImpliedRefs checks columns that name another table's key but declare no
// foreign key against it.
//
// Only exact name matches against a primary key are followed. A looser rule
// would compare columns that merely look related and report orphans between
// things that were never meant to line up — a false alarm here costs more than
// a miss, because it is the kind that makes people stop reading.
func findImpliedRefs(ctx context.Context, wh *warehouse.Warehouse, q func(string) string, schema *modelgen.Schema) []ImpliedRef {
	keyOwner := map[string]string{} // primary-key column name → table
	for _, t := range schema.Tables {
		if t.PrimaryKey != "" {
			keyOwner[strings.ToLower(t.PrimaryKey)] = t.Name
		}
	}

	var out []ImpliedRef
	for _, t := range schema.Tables {
		declared := map[string]bool{}
		for _, fk := range t.ForeignKeys {
			declared[strings.ToLower(fk.Column)] = true
		}
		for _, c := range t.Columns {
			name := strings.ToLower(c.Name)
			owner, ok := keyOwner[name]
			if !ok || owner == t.Name || declared[name] || strings.EqualFold(c.Name, t.PrimaryKey) {
				continue
			}
			total := scalarInt(ctx, wh, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL",
				q(t.Name), q(c.Name)))
			if total == 0 {
				continue
			}
			orphans := scalarInt(ctx, wh, fmt.Sprintf(
				"SELECT count(*) FROM %s c WHERE c.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s p WHERE p.%s = c.%s)",
				q(t.Name), q(c.Name), q(owner), q(c.Name), q(c.Name)))
			if orphans > 0 {
				out = append(out, ImpliedRef{
					Table: t.Name, Column: c.Name, RefTable: owner, RefColumn: c.Name,
					Orphans: orphans, Total: total,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Orphans > out[j].Orphans })
	return out
}
