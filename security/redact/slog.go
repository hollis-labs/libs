package redact

import (
	"context"
	"fmt"
	"log/slog"
)

// Handler wraps next so that nothing sensitive reaches the log: the message goes
// through [Redactor.Text], an attribute with a sensitive key loses its value, and
// string, error and stringer values are redacted. Groups are followed.
func (r *Redactor) Handler(next slog.Handler) slog.Handler { return &handler{r: r, next: next} }

type handler struct {
	r    *Redactor
	next slog.Handler
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Text(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = h.attr(a)
	}
	return &handler{r: h.r, next: h.next.WithAttrs(red)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{r: h.r, next: h.next.WithGroup(name)}
}

func (h *handler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup && h.r.piiName(a.Key) {
		return slog.String(a.Key, h.r.marker)
	}
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		red := make([]slog.Attr, len(group))
		for i, g := range group {
			red[i] = h.attr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(red...)}
	}
	if h.r.sensitiveName(a.Key) && v.Kind() != slog.KindBool {
		if s := fmt.Sprint(v.Any()); !h.r.isPlaceholder(s) {
			return slog.String(a.Key, h.r.marker)
		}
		return slog.Attr{Key: a.Key, Value: v}
	}
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.r.Text(v.String()))
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, h.r.ErrorText(x))
		case fmt.Stringer:
			return slog.String(a.Key, h.r.Text(x.String()))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
