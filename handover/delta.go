package handover

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/engagement"
)

// DeltaRollup aggregates the gaps recorded across every engagement.
//
// This is the loop that separates a forward-deployed engineer from a
// consultant. A consultant finishes and leaves; an engineer feeds back what the
// product could not do, and the next delivery is cheaper. Nothing feeds back
// that nobody counted — an item written down once in a customer's repository
// and never looked at again is the same as not having written it.
//
// The signal is repetition. One customer wanting fiscal years starting in April
// is a workaround. Three is a missing feature, and the third time somebody
// hand-rolls it is the expensive one.
type DeltaRollup struct {
	Items []DeltaGroup `json:"items"`
	Seen  int          `json:"engagements"`
}

// DeltaGroup is one recurring gap and who hit it.
type DeltaGroup struct {
	Kind      string   `json:"kind"`
	What      string   `json:"what"`
	Customers []string `json:"customers"`
	Count     int      `json:"count"`
	Examples  []string `json:"workarounds,omitempty"`
}

// Rollup groups the delta items of several engagements.
//
// Grouping is by normalised text, which is crude and deliberately so: two
// engineers describing the same gap in different words will not group, and a
// clustering that guessed would merge things that are not the same. An
// under-counted gap is visible the next time somebody reads the list; a wrongly
// merged one quietly becomes a feature request for something nobody needed.
func Rollup(engagements []*engagement.Engagement) *DeltaRollup {
	byKey := map[string]*DeltaGroup{}
	for _, e := range engagements {
		for _, d := range e.Delta {
			key := normalise(d.Kind) + "\x00" + normalise(d.What)
			g, ok := byKey[key]
			if !ok {
				g = &DeltaGroup{Kind: orUnknown(d.Kind), What: d.What}
				byKey[key] = g
			}
			if !contains(g.Customers, e.Customer) {
				g.Customers = append(g.Customers, e.Customer)
				g.Count++
			}
			if d.Workaround != "" && !contains(g.Examples, d.Workaround) {
				g.Examples = append(g.Examples, d.Workaround)
			}
		}
	}
	r := &DeltaRollup{Seen: len(engagements)}
	for _, g := range byKey {
		sort.Strings(g.Customers)
		r.Items = append(r.Items, *g)
	}
	// Most-repeated first: the ordering is the recommendation.
	sort.Slice(r.Items, func(i, j int) bool {
		if r.Items[i].Count != r.Items[j].Count {
			return r.Items[i].Count > r.Items[j].Count
		}
		return r.Items[i].What < r.Items[j].What
	})
	return r
}

// WriteMarkdown renders the rollup as a queue of work for the core product.
func (r *DeltaRollup) WriteMarkdown(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	p("# What the product could not do")
	p("")
	p("Gaps recorded across %d engagement(s), most repeated first.", r.Seen)

	if len(r.Items) == 0 {
		p("")
		p("Nothing recorded. Either the product fitted every delivery — unlikely — or")
		p("the `delta:` section of the engagement files is not being filled in, which")
		p("is the same as not having one: the next engineer pays the same cost again.")
		return
	}

	var repeated, once []DeltaGroup
	for _, g := range r.Items {
		if g.Count > 1 {
			repeated = append(repeated, g)
		} else {
			once = append(once, g)
		}
	}

	if len(repeated) > 0 {
		p("")
		p("## Hit more than once — build these")
		p("")
		p("A gap that recurs has stopped being a customer's peculiarity. Each of these")
		p("has now been worked around by hand more than once.")
		p("")
		for _, g := range repeated {
			p("### %s × %d", g.What, g.Count)
			p("")
			p("- **kind** %s", g.Kind)
			p("- **hit by** %s", strings.Join(g.Customers, ", "))
			if len(g.Examples) > 0 {
				p("- **worked around with** %s", strings.Join(quoteAll(g.Examples), ", "))
			}
			p("")
		}
	}

	if len(once) > 0 {
		p("## Seen once")
		p("")
		p("Not yet evidence of anything. Worth reading before the next delivery — the")
		p("second occurrence is what you want to notice, and you will only notice it")
		p("if you have read the first.")
		p("")
		for _, g := range once {
			p("- **%s** — %s *(%s)*", g.Kind, g.What, strings.Join(g.Customers, ", "))
		}
	}
}

// Summary is the line for a terminal.
func (r *DeltaRollup) Summary() string {
	if len(r.Items) == 0 {
		return fmt.Sprintf("no gaps recorded across %d engagement(s)", r.Seen)
	}
	repeated := 0
	for _, g := range r.Items {
		if g.Count > 1 {
			repeated++
		}
	}
	if repeated == 0 {
		return fmt.Sprintf("%d gap(s) across %d engagement(s), none yet repeated", len(r.Items), r.Seen)
	}
	return fmt.Sprintf("%d gap(s) across %d engagement(s) — %d hit more than once and should be built",
		len(r.Items), r.Seen, repeated)
}

// FindEngagements collects engagement.yaml files under root. Directories that
// cannot be read are skipped rather than aborting the walk: a rollup over
// twenty customers should not fail because one has odd permissions.
func FindEngagements(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "target", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "engagement.yaml" {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = "`" + s + "`"
	}
	return out
}
