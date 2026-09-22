// Package corpus is the half of the product that is not the warehouse.
//
// Everything else here answers questions with numbers: a signed semantic model
// compiles to SQL, the SQL runs, a figure comes back. That is the right answer
// to "what was revenue last quarter" and no answer at all to "why did we start
// excluding refunds", "which regions are in the APAC rollup", or "who decided
// that a trial counts as a customer". Those live in documents — the metric
// definition memo, the data dictionary, the ticket where somebody argued about
// it — and until now the product could not read one.
//
// The claim worth testing is that the two halves together answer questions
// neither half can. A question purely about numbers is better served by SQL
// alone, and a question purely about prose is better served by handing the
// prose to a model. Neither of those needs this package. What needs it is a
// question whose answer is a number that only means something once you know
// which definition produced it — which is, not coincidentally, every question
// anybody argues about after a report goes out.
//
// # Why this is not the document pipeline next door
//
// There is a full extraction pipeline in the sibling repositories — chunk, ask
// a model to pull out entities and relations, verify, queue the conflicts for a
// human, load the graph. It is not used here, on purpose and against the
// instinct to reuse it.
//
// It was measured on 2026-09-14 against the obvious baseline — put the whole
// corpus in a model's context and ask — and it lost: seven minutes against
// fourteen seconds, with more configuration and worse recall. The measurement
// was on 67 documents, 1.19 MB. That verdict is about extraction, and it does
// not transfer to retrieval: storing passages and finding them again is cheap,
// deterministic, needs no model in the critical path, and is exactly what is
// needed to cite a definition.
//
// So the corpus here is retrieval, not extraction. Documents go in as chunks
// with their provenance attached and come back out with it. If cross-source
// answering turns out to be worth anything, extraction can be added later on
// top of a corpus that already exists; adding it first would be paying the
// expensive half of a bet that has not been won yet.
package corpus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Collection is where corpus documents live in the brain, kept apart from
// whatever else a deployment stores there.
const Collection = "di_corpus"

// Embedder is what turns text into a vector. It is CortexDB's interface,
// re-exported so a caller can wire one without importing the store directly.
type Embedder = cortexdb.Embedder

// Store is the document half of an answer.
type Store struct {
	db *cortexdb.DB
}

// Open opens (or creates) the corpus at path.
//
// A nil embedder is a supported configuration, not a degraded one that fails
// later: the store refuses to ingest anything without an embedder, so nil gets
// LexicalEmbedder rather than a deferred error. See lexical.go for what that
// costs and why it is the right default.
func Open(path string, emb cortexdb.Embedder) (*Store, error) {
	if emb == nil {
		emb = LexicalEmbedder{}
	}
	cfg := cortexdb.DefaultConfig(path)
	cfg.Dimensions = emb.Dim()
	db, err := cortexdb.Open(cfg, cortexdb.WithEmbedder(emb))
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Document is one thing somebody wrote down.
type Document struct {
	ID    string
	Title string
	Body  string
	// Kind says what sort of document this is — "definition", "dictionary",
	// "ticket", "runbook". It is free text because no list of kinds survives
	// contact with a second customer, and it is carried through to every
	// citation so a reader can weigh a metric memo differently from a ticket.
	Kind string
	// Metric, when set, names the metric this document is about. It is the
	// join to the semantic model, and the reason a brief can say "this figure
	// uses the definition that document argued for" rather than only listing
	// passages that happened to match the words.
	Metric string
}

// Add ingests one document. Re-adding the same ID replaces it, so a corpus can
// be rebuilt from a directory without accumulating duplicates of every file.
func (s *Store) Add(ctx context.Context, d Document) (chunks int, err error) {
	if strings.TrimSpace(d.ID) == "" {
		return 0, fmt.Errorf("corpus: a document needs an id")
	}
	if strings.TrimSpace(d.Body) == "" {
		return 0, fmt.Errorf("corpus: %s is empty — nothing to cite", d.ID)
	}
	meta := map[string]string{
		"kind":  strings.TrimSpace(d.Kind),
		"title": d.Title,
		"at":    time.Now().UTC().Format(time.RFC3339),
	}
	if m := strings.TrimSpace(d.Metric); m != "" {
		meta["metric"] = m
	}
	// See chunking.go: the store measures chunk size in whitespace-separated
	// runs, so the number that gives a readable passage depends on the script.
	words := chunkWordsFor(d.Body)
	res, err := s.db.InsertGraphDocument(ctx,
		cortexdb.GraphRAGDocument{ID: d.ID, Title: d.Title, Content: d.Body, Metadata: meta},
		cortexdb.GraphRAGIngestOptions{Collection: Collection, ChunkSize: words, ChunkOverlap: chunkOverlapFor(words)})
	if err != nil {
		return 0, fmt.Errorf("corpus: ingest %s: %w", d.ID, err)
	}
	return len(res.ChunkNodeIDs), nil
}

// AddFile reads a file and ingests it, taking the id from the file name and the
// title from a leading markdown heading when there is one.
func (s *Store) AddFile(ctx context.Context, path, kind, metric string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	body := string(b)
	id := filepath.Base(path)
	title := id
	if first, _, ok := strings.Cut(body, "\n"); ok {
		if h := strings.TrimSpace(strings.TrimLeft(first, "# ")); h != "" && strings.HasPrefix(first, "#") {
			title = h
		}
	}
	return s.Add(ctx, Document{ID: id, Title: title, Body: body, Kind: kind, Metric: metric})
}

// Passage is one citable piece of a document.
type Passage struct {
	DocumentID string
	Text       string
	Score      float64
}

// Recall returns the passages that bear on a question, best first.
//
// An empty result is an ordinary answer, not an error: most questions about
// numbers have nothing written about them, and a store that manufactured a
// citation for those would be worse than one that says nothing.
func (s *Store) Recall(ctx context.Context, question string, k int) ([]Passage, error) {
	if k <= 0 {
		k = 4
	}
	// Retrieve wider than the caller wants to see. The relevance floor below
	// removes passages that share no vocabulary with the question, and if the
	// store is only asked for k candidates then a floor that rejects the first
	// one returns nothing — asking for one passage found nothing where asking
	// for two found the right one. A filter is supposed to raise precision, not
	// to make recall depend on how many results somebody wanted printed.
	candidates := k * 4
	if candidates < 12 {
		candidates = 12
	}
	res, err := s.db.SearchGraphRAG(ctx, question, cortexdb.GraphRAGQueryOptions{
		Collection: Collection,
		TopK:       candidates,
		// One document rarely deserves every slot: a data dictionary that
		// mentions revenue in eight places would otherwise crowd out the memo
		// that explains why the definition changed.
		PerDocumentLimit: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("corpus: recall %q: %w", question, err)
	}
	out := make([]Passage, 0, len(res.Chunks))
	for _, c := range res.Chunks {
		// See relevance.go: top-k always returns k things, and a passage that
		// shares no vocabulary with the question is not a citation.
		if !bears(question, c.Content) {
			continue
		}
		out = append(out, Passage{DocumentID: c.DocumentID, Text: trimmed(c.Content), Score: c.Score})
		if len(out) == k {
			break
		}
	}
	return out, nil
}
