package agentdef

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
)

// GeneratedSpan is one
//
//	<!-- agentdef:generated source=<path> hash=sha256:<hex> -->
//	...
//	<!-- /agentdef:generated -->
//
// region in a definition's body. The marker convention is provisional.
type GeneratedSpan struct {
	Source     string // path relative to the definition's layer root
	PinnedHash string
}

// StaleSpan is a span whose source no longer hashes to the pinned value.
type StaleSpan struct {
	Span         GeneratedSpan
	ComputedHash string
}

var (
	spanOpen  = regexp.MustCompile(`<!--\s*agentdef:generated\b([^>]*?)-->`)
	spanClose = regexp.MustCompile(`<!--\s*/agentdef:generated\s*-->`)
)

// ParseGeneratedSpans extracts every generated span from a markdown body. An
// unclosed, nested, stray-closing or attribute-less span is an error.
func ParseGeneratedSpans(body string) ([]GeneratedSpan, error) {
	var spans []GeneratedSpan
	opens := spanOpen.FindAllStringSubmatchIndex(body, -1)
	closes := spanClose.FindAllStringIndex(body, -1)

	type ev struct {
		pos   int
		open  bool
		attrs string
	}
	var evs []ev
	for _, o := range opens {
		evs = append(evs, ev{pos: o[0], open: true, attrs: body[o[2]:o[3]]})
	}
	for _, c := range closes {
		evs = append(evs, ev{pos: c[0]})
	}
	for i := range evs { // insertion sort by position; event counts are tiny
		for j := i; j > 0 && evs[j].pos < evs[j-1].pos; j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}

	inside := false
	for _, e := range evs {
		switch {
		case e.open && inside:
			return nil, errors.New("agentdef: nested agentdef:generated span")
		case e.open:
			inside = true
			s, err := parseSpanAttrs(e.attrs)
			if err != nil {
				return nil, err
			}
			spans = append(spans, s)
		case !inside:
			return nil, errors.New("agentdef: /agentdef:generated with no opening marker")
		default:
			inside = false
		}
	}
	if inside {
		return nil, errors.New("agentdef: agentdef:generated span is never closed")
	}
	return spans, nil
}

func parseSpanAttrs(attrs string) (GeneratedSpan, error) {
	var s GeneratedSpan
	for _, f := range strings.Fields(attrs) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return s, fmt.Errorf("agentdef: generated span: malformed attribute %q", f)
		}
		switch k {
		case "source":
			s.Source = v
		case "hash":
			s.PinnedHash = v
		default:
			return s, fmt.Errorf("agentdef: generated span: unknown attribute %q", k)
		}
	}
	if s.Source == "" || s.PinnedHash == "" {
		return s, errors.New("agentdef: generated span needs both source= and hash=")
	}
	return s, nil
}

// CheckSpans recomputes each span's source hash under layerRoot and returns
// the spans that drifted. It is the comparison core; where spans come from
// (inline markers today) is the caller's business.
func CheckSpans(fsys fs.FS, layerRoot string, spans []GeneratedSpan) ([]StaleSpan, error) {
	if layerRoot == "" {
		layerRoot = "."
	}
	var stale []StaleSpan
	for _, s := range spans {
		if !fs.ValidPath(s.Source) {
			return nil, fmt.Errorf("agentdef: generated span source %q must be a relative path inside the layer root", s.Source)
		}
		data, err := fs.ReadFile(fsys, path.Join(layerRoot, s.Source))
		if err != nil {
			return nil, fmt.Errorf("agentdef: generated span source %q: %w", s.Source, err)
		}
		if got := sha256Hex(data); got != s.PinnedHash {
			stale = append(stale, StaleSpan{Span: s, ComputedHash: got})
		}
	}
	return stale, nil
}

// CheckGenerated recomputes each generated span's source hash and reports
// drift. A definition with no spans passes trivially.
func CheckGenerated(fsys fs.FS, layerRoot string, d *Definition) ([]StaleSpan, error) {
	spans, err := ParseGeneratedSpans(d.Body)
	if err != nil {
		return nil, err
	}
	return CheckSpans(fsys, layerRoot, spans)
}
