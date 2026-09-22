package corpus

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// The store will not ingest a document without an embedder, so "retrieval falls
// back to BM25 when no embedding endpoint is configured" — which is what this
// package claimed, and what its Open signature implied by accepting nil — was
// not true. Ingestion failed outright with `embedder not configured`, and the
// first thing that discovered it was running the command.
//
// There were two honest ways out: refuse at Open and tell the operator to
// configure an endpoint, or ship an embedder that needs none. This is the
// second, because the first makes the document half unavailable to exactly the
// deployments most likely to want to try it before paying for anything.
//
// What it is: the hashing trick over word and character n-grams. Each feature
// is hashed into one of 256 buckets and the vector is L2-normalized, so cosine
// similarity between two texts approximates how much vocabulary they share.
//
// What it is not: semantic. "revenue" and "turnover" are as unrelated to it as
// "revenue" and "rhubarb". It exists so that retrieval degrades to something
// real rather than to an error, and BM25 — which the store runs alongside it
// and which is genuinely good at this — does the work that matters. Configure a
// real embedding endpoint and this is not used.

// LexicalDim is the width of the hashed feature space. 256 is enough to keep
// collisions between the few thousand distinct tokens of a document corpus
// rare, and small enough that the vectors cost nothing to store.
const LexicalDim = 256

// LexicalEmbedder is the no-endpoint embedder described above.
type LexicalEmbedder struct{}

func (LexicalEmbedder) Dim() int { return LexicalDim }

func (e LexicalEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return lexicalVector(text), nil
}

func (e LexicalEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = lexicalVector(t)
	}
	return out, nil
}

func lexicalVector(text string) []float32 {
	v := make([]float32, LexicalDim)
	for _, tok := range tokens(text) {
		add(v, "w:"+tok, 1)
		// Character trigrams carry two things whole words do not: morphology
		// ("refund" ~ "refunds") and CJK, where a token is often one or two
		// characters and word splitting does not apply at all.
		r := []rune(tok)
		for i := 0; i+3 <= len(r); i++ {
			add(v, "c:"+string(r[i:i+3]), 0.5)
		}
		if len(r) == 2 {
			add(v, "c:"+tok, 0.5)
		}
	}
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

func add(v []float32, feature string, weight float32) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(feature))
	v[h.Sum32()%LexicalDim] += weight
}

// tokens splits on anything that is not a letter or a digit, and treats each
// CJK character as its own token — there are no spaces to split on there, and a
// per-character token plus the trigrams above is what makes a Chinese document
// findable at all.
func tokens(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}
