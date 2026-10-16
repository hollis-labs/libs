package redaction

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const marker = "MARKER-value-0123456789"

func run(t *testing.T, opts ...ExporterOption) tracetest.SpanStubs {
	t.Helper()
	mem := &collect{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(Exporter(mem, opts...)))
	_, sp := tp.Tracer("t").Start(context.Background(), "call "+marker,
		trace.WithAttributes(
			attribute.String("gen_ai.content.prompt", "tell me "+marker),
			attribute.String("gen_ai.request.model", "m"),
			attribute.String("note", "k="+marker),
			attribute.StringSlice("tags", []string{"ok", marker}),
		))
	sp.AddEvent("gen_ai.content.completion", trace.WithAttributes(attribute.String("gen_ai.completion", marker)))
	sp.AddEvent("other", trace.WithAttributes(attribute.String("gen_ai.content.prompt", marker), attribute.String("n", marker)))
	sp.SetStatus(codes.Error, "failed with "+marker)
	sp.End()
	_ = tp.Shutdown(context.Background())
	return tracetest.SpanStubsFromReadOnlySpans(mem.spans)
}

func TestExporterDropsDenylistedKeysByDefault(t *testing.T) {
	t.Setenv("HOLLIS_OTEL_REDACT_PROMPTS", "")
	spans := run(t)
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	s := spans[0]
	for _, kv := range s.Attributes {
		if kv.Key == "gen_ai.content.prompt" {
			t.Fatal("denylisted span attribute exported")
		}
	}
	if len(s.Attributes) != 3 {
		t.Fatalf("non-denylisted attributes lost: %v", s.Attributes)
	}
	if len(s.Events) != 2 || len(s.Events[0].Attributes) != 0 {
		t.Fatalf("denylisted event kept its attributes: %+v", s.Events)
	}
	for _, kv := range s.Events[1].Attributes {
		if kv.Key == "gen_ai.content.prompt" {
			t.Fatal("denylisted event attribute exported")
		}
	}
	// Without a value redactor other strings pass through unchanged.
	if !strings.Contains(s.Name, marker) {
		t.Fatalf("name altered without a redactor: %q", s.Name)
	}
}

func TestExporterRespectsDisableSwitch(t *testing.T) {
	t.Setenv("HOLLIS_OTEL_REDACT_PROMPTS", "false")
	s := run(t)[0]
	if len(s.Attributes) != 4 {
		t.Fatalf("attributes = %v, want all 4 with redaction disabled", s.Attributes)
	}
}

func TestExporterAppliesValueRedactor(t *testing.T) {
	t.Setenv("HOLLIS_OTEL_REDACT_PROMPTS", "")
	s := run(t, WithValueRedactor(func(v string) string { return strings.ReplaceAll(v, marker, "[REDACTED]") }))[0]
	dump := s.Name + s.Status.Description
	for _, kv := range s.Attributes {
		dump += kv.Value.Emit()
	}
	for _, ev := range s.Events {
		dump += ev.Name
		for _, kv := range ev.Attributes {
			dump += kv.Value.Emit()
		}
	}
	if strings.Contains(dump, marker) || !strings.Contains(dump, "[REDACTED]") {
		t.Fatalf("value redactor not applied everywhere: %s", dump)
	}
}

func TestExporterLeavesOriginalSpansUntouched(t *testing.T) {
	t.Setenv("HOLLIS_OTEL_REDACT_PROMPTS", "")
	var seen []sdktrace.ReadOnlySpan
	rec := &capture{onExport: func(sp []sdktrace.ReadOnlySpan) { seen = sp }}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(rec)),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(Exporter(tracetest.NewInMemoryExporter()))))
	_, sp := tp.Tracer("t").Start(context.Background(), "x", trace.WithAttributes(attribute.String("gen_ai.content.prompt", "p")))
	sp.End()
	if len(seen) != 1 || len(seen[0].Attributes()) != 1 {
		t.Fatalf("original span was mutated: %+v", seen)
	}
}

type capture struct{ onExport func([]sdktrace.ReadOnlySpan) }

func (c *capture) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	c.onExport(s)
	return nil
}
func (c *capture) Shutdown(context.Context) error { return nil }

// collect keeps what it was given; InMemoryExporter forgets it on Shutdown.
type collect struct{ spans []sdktrace.ReadOnlySpan }

func (c *collect) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	c.spans = append(c.spans, s...)
	return nil
}
func (c *collect) Shutdown(context.Context) error { return nil }
