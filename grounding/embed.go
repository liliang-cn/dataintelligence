package grounding

import (
	"context"
	"math"
	"os"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/llm"
)

// embedderFromEnv builds a dense embedder from DI_EMBED_BASE_URL / DI_EMBED_API_KEY
// / DI_EMBED_MODEL (any OpenAI-compatible /embeddings endpoint, e.g. DashScope
// text-embedding-v4). Returns (nil, nil) when unconfigured so the grounder
// degrades cleanly to lexical-only retrieval.
func embedderFromEnv() (domain.EmbedderProvider, error) {
	base, key, model := os.Getenv("DI_EMBED_BASE_URL"), os.Getenv("DI_EMBED_API_KEY"), os.Getenv("DI_EMBED_MODEL")
	if base == "" || key == "" || model == "" {
		return nil, nil
	}
	return llm.NewOpenAIEmbedder(base, key, model)
}

// unit normalizes a vector in place and returns it (so cosine == dot product).
func unit(v []float64) []float64 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	n = math.Sqrt(n)
	if n == 0 {
		return v
	}
	for i := range v {
		v[i] /= n
	}
	return v
}

// dot is the cosine similarity of two unit vectors.
func dot(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// embedUnit embeds one text and returns its unit vector.
func embedUnit(ctx context.Context, e domain.EmbedderProvider, text string) ([]float64, error) {
	v, err := e.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	return unit(v), nil
}

// minmax scales scores to [0,1]. A flat set maps to all-zeros (no signal).
func minmax(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	if len(in) == 0 {
		return out
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range in {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := hi - lo
	for k, v := range in {
		if span == 0 {
			out[k] = 0
		} else {
			out[k] = (v - lo) / span
		}
	}
	return out
}
