// Package llm is the whole of this platform's dependence on a language model:
// ask it something, or embed a string.
//
// It used to be an agent framework. That framework does a great deal — tool
// loops, planners, memory, teams — and none of it was reachable from here: the
// call sites wanted `Ask(prompt) → text` and `Embed(text) → vector`, and a
// framework is a strange way to spell two HTTP calls. The cost was not the
// bytes. It was that a data platform, the thing whose job is to be right about
// numbers, carried an AI agent framework in its dependency graph — so upgrading
// the agent side could break the data side, and a customer reading the module
// list had to be told why.
//
// Both endpoints are the OpenAI shape, which every gateway and local runtime
// (Ollama, vLLM, LM Studio, DashScope) speaks. Nothing here is provider-aware
// beyond that.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Service asks a chat model one question at a time. No history, no tools —
// every caller in this repo is doing single-shot classification or drafting,
// and a conversation object none of them use is a conversation object that
// silently accumulates state.
type Service struct {
	base  string
	key   string
	model string
	http  *http.Client
}

// NewOpenAIFromEnv reads LLM_BASE_URL / LLM_API_KEY / LLM_MODEL, falling back to
// the LLM_BASE / LLM_KEY spelling the product uses.
//
// Unconfigured is an error, not a silent no-op: every caller has a
// deterministic path to fall back to, and it can only choose it if it is told.
func NewOpenAIFromEnv() (*Service, error) {
	base := firstOf("LLM_BASE_URL", "LLM_BASE", "OPENAI_BASE_URL")
	key := firstOf("LLM_API_KEY", "LLM_KEY", "OPENAI_API_KEY")
	model := firstOf("LLM_MODEL", "OPENAI_MODEL")
	if base == "" || model == "" {
		return nil, fmt.Errorf("LLM 未配置(需要 LLM_BASE_URL/LLM_BASE 与 LLM_MODEL)")
	}
	return &Service{
		base:  strings.TrimSuffix(base, "/"),
		key:   key,
		model: model,
		// 一次分类或起草不该拖着一个请求不放。上游卡住时,调用方的确定性
		// 回退路径比一个永远不返回的正确答案有用。
		http: &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// New builds a service explicitly (tests, and callers that already hold config).
func New(base, key, model string) *Service {
	return &Service{
		base:  strings.TrimSuffix(base, "/"),
		key:   key,
		model: model,
		http:  &http.Client{Timeout: 120 * time.Second},
	}
}

// Model reports which model this service talks to.
func (s *Service) Model() string { return s.model }

// Ask sends one prompt and returns the text.
func (s *Service) Ask(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":    s.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := s.post(ctx, "/chat/completions", body, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("模型没有返回任何内容")
	}
	return out.Choices[0].Message.Content, nil
}

// Embedder turns text into a dense vector. Kept as an interface so grounding
// can hold "no embedder configured" as nil and fall back to lexical retrieval.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	// EmbedBatch is one request for many texts. Building a grounding index
	// means embedding every dimension value in the warehouse; one request per
	// value turns a minute into an hour and bills like it.
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)
}

type embedder struct{ s *Service }

// NewOpenAIEmbedder builds an embedder against any OpenAI-shaped /embeddings.
func NewOpenAIEmbedder(base, key, model string) (Embedder, error) {
	if base == "" || model == "" {
		return nil, fmt.Errorf("embedder 未配置")
	}
	return embedder{New(base, key, model)}, nil
}

func (e embedder) Embed(ctx context.Context, text string) ([]float64, error) {
	body, _ := json.Marshal(map[string]any{"model": e.s.model, "input": text})
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := e.s.post(ctx, "/embeddings", body, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding 服务没有返回向量")
	}
	return out.Data[0].Embedding, nil
}

func (e embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{"model": e.s.model, "input": texts})
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := e.s.post(ctx, "/embeddings", body, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("要了 %d 个向量,回来 %d 个", len(texts), len(out.Data))
	}
	// 按 index 归位,不按返回顺序:规范允许乱序,而错位的向量不会报错,
	// 它只是让「北京」匹配到「上海」——一个没有任何东西会发现的错误。
	vecs := make([][]float64, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("embedding 返回了越界的 index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

func (s *Service) post(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("%s 返回了无法解析的响应(HTTP %d):%w", path, res.StatusCode, err)
	}
	if res.StatusCode >= 400 {
		// 正文里通常有 error.message,交给调用方去读;这里只保证状态码不被吞掉。
		return fmt.Errorf("%s HTTP %d", path, res.StatusCode)
	}
	return nil
}

func firstOf(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
