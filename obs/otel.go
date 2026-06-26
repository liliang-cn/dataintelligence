package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// This file makes the observability real OpenTelemetry: a TracerProvider, a span
// tree per request (trace_id → compile → plan → execute), and W3C trace-context
// propagation so a trace started by a client continues, with the same trace_id,
// inside this process across the MCP HTTP boundary.
//
// Spans are no-ops until InitOTel is called (the global provider defaults to a
// no-op tracer), so instrumentation is free when tracing is off.

// jsonExporter is a dependency-free SpanExporter: it writes each finished span as
// one JSON line, so a real OTel span tree is visible without standing up a
// collector. Point OTEL at a real collector in production by swapping this for
// an OTLP exporter — the instrumentation does not change.
type jsonExporter struct {
	w  io.Writer
	mu sync.Mutex
}

func (e *jsonExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range spans {
		rec := map[string]any{
			"trace_id":    s.SpanContext().TraceID().String(),
			"span_id":     s.SpanContext().SpanID().String(),
			"name":        s.Name(),
			"duration_ms": s.EndTime().Sub(s.StartTime()).Milliseconds(),
			"attrs":       attrsToMap(s.Attributes()),
		}
		if s.Parent().HasSpanID() {
			rec["parent_span_id"] = s.Parent().SpanID().String()
		}
		b, _ := json.Marshal(rec)
		fmt.Fprintln(e.w, "otel "+string(b))
	}
	return nil
}

func (e *jsonExporter) Shutdown(context.Context) error { return nil }

func attrsToMap(kvs []attribute.KeyValue) map[string]any {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value.AsInterface()
	}
	return m
}

// InitOTel installs a real TracerProvider (synchronous JSON exporter to stderr)
// and the W3C TraceContext propagator. Returns a shutdown func. Call once at
// startup; until then all spans are no-ops.
func InitOTel(service string) (func(context.Context) error, error) {
	exp := &jsonExporter{w: os.Stderr}
	res, err := resource.New(context.Background(), resource.WithAttributes(
		attribute.String("service.name", service)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

// Tracer returns the platform tracer (no-op until InitOTel runs).
func Tracer() trace.Tracer { return otel.Tracer("dataintelligence") }

// InjectMap writes the current span's W3C traceparent into a string carrier
// (e.g. before an outgoing request).
func InjectMap(ctx context.Context, carrier map[string]string) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(carrier))
}

// ExtractMap reconstructs the remote span context from a string carrier.
func ExtractMap(ctx context.Context, carrier map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}

// ExtractHTTP continues a trace carried in inbound HTTP headers (traceparent),
// so a server span nests under the client's trace — real cross-process linkage.
func ExtractHTTP(ctx context.Context, h http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(h))
}
