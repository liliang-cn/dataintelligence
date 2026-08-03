package handover

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/liliang-cn/dataintelligence/engagement"
)

// Pack is the manifest of what was handed over.
type Pack struct {
	Customer string `json:"customer"`
	Owner    string `json:"owner"`
	Started  string `json:"started"`
	Archive  string `json:"archive"`

	Files   []PackedFile `json:"files"`
	Missing []string     `json:"missing,omitempty"`
	// Secrets are the environment variables the delivery needs and the archive
	// deliberately does not contain.
	Secrets []string `json:"secrets,omitempty"`
	// Versions pin what produced this. A delivery that cannot say which
	// compiler generated its SQL cannot be reproduced, and "it used to give a
	// different number" is unanswerable a year later.
	Versions map[string]string `json:"versions"`
}

// PackedFile is one artefact with its digest.
type PackedFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	What   string `json:"what"`
}

// artefacts is what a delivery consists of, in the order a person reads them.
//
// The list is fixed rather than "whatever is in the directory": a package built
// by globbing quietly ships whatever was lying around — a scratch DSN file, a
// half-edited model — and quietly omits whatever was never generated. Naming
// them means the manifest can say what is *missing*, which is the half that
// matters when somebody inherits this.
var artefacts = []struct{ path, what string }{
	{"engagement.yaml", "the delivery itself: databases, model, evalset, and what the platform could not do"},
	{"out/survey.md", "what the warehouse actually contained on day one"},
	{"out/delivery.md", "what was built and what proves it"},
	{"RUNBOOK.md", "for whoever inherits this"},
	{".github/workflows/di-gate.yml", "the gate, so it runs without being remembered"},
}

// Build writes a reproducible archive of one engagement.
//
// What makes it a handover rather than a zip is the manifest: every file with
// its digest, every secret it needs by name and not by value, and the versions
// of the compiler and CLI that produced the numbers. Without those a delivery
// is a folder, and a folder cannot answer "is this still the thing you signed
// off?" — which is the only question anyone asks of it later.
func Build(e *engagement.Engagement, out string) (*Pack, error) {
	dir := e.Dir()
	p := &Pack{
		Customer: e.Customer, Owner: e.Owner, Started: e.Started,
		Archive: out, Versions: versions(),
	}

	// Models and reconciliation sets are per-database, so they come off the
	// engagement rather than off a fixed list.
	files := append([]struct{ path, what string }{}, artefacts...)
	seen := map[string]bool{}
	for _, db := range e.Databases {
		for _, f := range []struct{ path, what string }{
			{db.Model, "the semantic model for " + db.ID + " — the definitions, as an executable asset"},
			{db.Recon, "the control behind every metric in " + db.ID},
		} {
			if f.path == "" {
				continue
			}
			rel, err := filepath.Rel(dir, f.path)
			if err != nil {
				rel = f.path
			}
			if !seen[rel] {
				seen[rel] = true
				files = append(files, struct{ path, what string }{rel, f.what})
			}
		}
		p.Secrets = append(p.Secrets, db.Vars...)
	}
	if e.Evalset != "" {
		if rel, err := filepath.Rel(dir, e.Evalset); err == nil {
			files = append(files, struct{ path, what string }{rel, "the labelled questions the delivery is graded on"})
		}
	}
	p.Secrets = dedupeStrings(p.Secrets)

	f, err := os.Create(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, a := range files {
		full := filepath.Join(dir, a.path)
		info, err := os.Stat(full)
		if err != nil {
			p.Missing = append(p.Missing, fmt.Sprintf("%s — %s", a.path, a.what))
			continue
		}
		body, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		p.Files = append(p.Files, PackedFile{
			Path: a.path, Bytes: info.Size(), SHA256: hex.EncodeToString(sum[:]), What: a.what,
		})
		// A fixed mode and no timestamp: the same inputs must produce the same
		// archive, or the digest of the delivery changes every time somebody
		// rebuilds it and stops meaning anything.
		if err := tw.WriteHeader(&tar.Header{
			Name: a.path, Mode: 0o644, Size: int64(len(body)), Format: tar.FormatPAX,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}

	manifest := p.WriteMarkdown()
	if err := tw.WriteHeader(&tar.Header{
		Name: "MANIFEST.md", Mode: 0o644, Size: int64(len(manifest)), Format: tar.FormatPAX,
	}); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(tw, manifest); err != nil {
		return nil, err
	}
	return p, nil
}

// WriteMarkdown renders the manifest that travels inside the archive.
func (p *Pack) WriteMarkdown() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("# Delivery package — %s", p.Customer)
	w("")
	w("Delivered by %s, engagement started %s.", orDash(p.Owner), orDash(p.Started))
	w("")
	w("Every file below is listed with its SHA-256. To check that what you have is")
	w("what was handed over, hash it and compare — that is the whole point of this")
	w("file, and it is the only way to answer \"is this still the version we signed")
	w("off?\" once a year has passed.")
	w("")
	w("| File | What it is | Bytes | SHA-256 |")
	w("|---|---|---:|---|")
	for _, f := range p.Files {
		w("| `%s` | %s | %d | `%s` |", f.Path, f.What, f.Bytes, f.SHA256[:16]+"…")
	}

	if len(p.Missing) > 0 {
		w("")
		w("## Not in this package (%d)", len(p.Missing))
		w("")
		w("These were never generated. That is a statement about the delivery, not")
		w("about the packaging — a handover missing its runbook or its acceptance")
		w("report is incomplete, and saying so here is cheaper than discovering it")
		w("when somebody needs one.")
		w("")
		for _, m := range p.Missing {
			w("- %s", m)
		}
	}

	if len(p.Secrets) > 0 {
		w("")
		w("## What this package deliberately does not contain")
		w("")
		w("Database credentials. The delivery reads them from the environment, and")
		w("these are the names it expects:")
		w("")
		for _, s := range p.Secrets {
			w("- `%s`", s)
		}
		w("")
		w("Set them and every command in the runbook works. They are not in the")
		w("archive because an archive gets emailed.")
	}

	w("")
	w("## What produced this")
	w("")
	w("| | |")
	w("|---|---|")
	keys := make([]string, 0, len(p.Versions))
	for k := range p.Versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w("| %s | `%s` |", k, p.Versions[k])
	}
	w("")
	w("The compiler version matters more than it looks: it decides the SQL behind")
	w("every metric. Two versions can both be right and still not agree to the")
	w("last decimal, and \"the number changed\" needs an answer.")
	return b.String()
}

// versions reads what is pinned into this binary. Build info is absent in some
// build modes, and reporting "unknown" is better than reporting nothing.
func versions() map[string]string {
	out := map[string]string{"di": "unknown", "semantic-go": "unknown", "go": "unknown"}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}
	out["go"] = info.GoVersion
	if info.Main.Version != "" {
		out["di"] = info.Main.Version
	}
	for _, d := range info.Deps {
		if strings.HasSuffix(d.Path, "/semantic-go") {
			out["semantic-go"] = d.Version
		}
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			out["di"] = s.Value
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
