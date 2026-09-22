package corpus

import (
	"context"
	"fmt"
	"sync"
)

// The product has two embedder interfaces and they disagree twice.
//
// This repository's own (llm.Embedder) returns []float64 and does not know its
// own dimension; the store's returns []float32 and must state it up front. The
// mismatch is not anybody's mistake — a caller building a grounding index never
// needs the dimension, and a vector store cannot allocate without it — but the
// two do not compose, and the metric index and the corpus must use the same
// endpoint or a deployment is paying for two.
//
// Adapt is the adapter, and the interesting part is Dim: the dimension is a
// property of the model on the other end of an HTTP call, not of the interface,
// so the only honest way to learn it is to ask once. That probe happens on
// first use and is remembered, because Dim is called per operation and a
// round trip per call would be absurd.

// Vectorizer is this repository's embedder shape: float64, no dimension.
type Vectorizer interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)
}

// Adapt turns one of this repository's embedders into one the store accepts.
// A nil Vectorizer adapts to nil, so "no embedding endpoint configured" stays
// one value all the way down rather than becoming a typed nil that passes a
// != nil check and panics later.
func Adapt(v Vectorizer) Embedder {
	if v == nil {
		return nil
	}
	return &adapter{v: v}
}

type adapter struct {
	v    Vectorizer
	once sync.Once
	dim  int
}

// probe asks the endpoint how wide its vectors are, once.
//
// A failure leaves dim at zero rather than retrying on every call: the store
// reports an unusable dimension clearly, and an embedding service that is down
// should surface as one error rather than as one per chunk.
func (a *adapter) probe() {
	a.once.Do(func() {
		v, err := a.v.Embed(context.Background(), "dimension probe")
		if err == nil {
			a.dim = len(v)
		}
	})
}

func (a *adapter) Dim() int {
	a.probe()
	return a.dim
}

func (a *adapter) Embed(ctx context.Context, text string) ([]float32, error) {
	v, err := a.v.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	return narrow(v), nil
}

func (a *adapter) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	vs, err := a.v.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(vs) != len(texts) {
		return nil, fmt.Errorf("corpus: embedder returned %d vectors for %d texts", len(vs), len(texts))
	}
	out := make([][]float32, len(vs))
	for i, v := range vs {
		out[i] = narrow(v)
	}
	return out, nil
}

// narrow is the float64 → float32 conversion. It loses precision that cosine
// similarity never had: the vectors come off the wire as decimal text with
// fewer significant digits than float32 carries.
func narrow(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}
