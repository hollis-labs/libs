package redaction

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// ExporterOption configures [Exporter].
type ExporterOption func(*exporter)

// WithKeyFilter sets the predicate that decides which attribute keys are
// dropped before export. It replaces [ShouldRedact] (the denylist plus the
// HOLLIS_OTEL_REDACT_PROMPTS switch). An event whose name matches has all its
// attributes dropped; the event itself is kept so the timeline survives.
func WithKeyFilter(drop func(key string) bool) ExporterOption {
	return func(e *exporter) { e.drop = drop }
}

// WithValueRedactor sets a function applied to every string the exporter
// forwards: string and string-slice attribute values, span and event names, and
// the status description. It is injected so this module needs no dependency on
// a redaction engine; pass security/redact's Redactor.String, for example.
// nil (the default) leaves values as they are.
func WithValueRedactor(redact func(string) string) ExporterOption {
	return func(e *exporter) { e.redact = redact }
}

// Exporter wraps next so spans are redacted before they leave the process.
// This is where redaction can actually happen: a [sdktrace.SpanProcessor] only
// sees immutable [sdktrace.ReadOnlySpan]s, so the exporter forwards redacted
// copies and leaves the originals untouched.
//
// With no options it drops the keys [ShouldRedact] names.
func Exporter(next sdktrace.SpanExporter, opts ...ExporterOption) sdktrace.SpanExporter {
	e := &exporter{next: next, drop: ShouldRedact}
	for _, o := range opts {
		o(e)
	}
	return e
}

type exporter struct {
	next   sdktrace.SpanExporter
	drop   func(string) bool
	redact func(string) string
}

func (e *exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = e.span(s)
	}
	return e.next.ExportSpans(ctx, out)
}

func (e *exporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

func (e *exporter) str(s string) string {
	if e.redact == nil {
		return s
	}
	return e.redact(s)
}

func (e *exporter) attrs(in []attribute.KeyValue) []attribute.KeyValue {
	if len(in) == 0 {
		return in
	}
	out := make([]attribute.KeyValue, 0, len(in))
	for _, kv := range in {
		if e.drop != nil && e.drop(string(kv.Key)) {
			continue
		}
		switch kv.Value.Type() {
		case attribute.STRING:
			kv.Value = attribute.StringValue(e.str(kv.Value.AsString()))
		case attribute.STRINGSLICE:
			vs := kv.Value.AsStringSlice()
			red := make([]string, len(vs))
			for j, v := range vs {
				red[j] = e.str(v)
			}
			kv.Value = attribute.StringSliceValue(red)
		}
		out = append(out, kv)
	}
	return out
}

func (e *exporter) span(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	stub := tracetest.SpanStubFromReadOnlySpan(s)
	stub.Name = e.str(stub.Name)
	stub.Attributes = e.attrs(stub.Attributes)
	stub.Status.Description = e.str(stub.Status.Description)
	for i := range stub.Events {
		ev := &stub.Events[i]
		if e.drop != nil && e.drop(ev.Name) {
			ev.Attributes = nil
		} else {
			ev.Attributes = e.attrs(ev.Attributes)
		}
		ev.Name = e.str(ev.Name)
	}
	for i := range stub.Links {
		stub.Links[i].Attributes = e.attrs(stub.Links[i].Attributes)
	}
	return stub.Snapshot()
}
