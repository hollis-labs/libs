package manifest

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Server declares a bundled file, not a launcher or shell invocation. Engines
// has exactly one key matching Runtime, with inclusive Min/Max bounds.
type Server struct {
	Runtime string               `json:"runtime"`
	Engines map[string]HostRange `json:"engines"`
	Entry   string               `json:"entry"`
}

// UI declares browser artifacts. Isolation is a preference, never permission;
// the host must explicitly accept or refuse it before activating browser code.
type UI struct {
	Bundle     string `json:"bundle"`
	Stylesheet string `json:"stylesheet,omitempty"`
	Isolation  string `json:"isolation"` // iframe or shared
}

// Hook is a declaration only. Catalog/admission, limits and grants are host-owned.
// Priority nil means 10; explicit zero remains zero. Timeout is milliseconds.
type Hook struct {
	Name     string `json:"name"`
	Priority *int   `json:"priority,omitempty"`
	Mode     string `json:"mode"`
	Timeout  int    `json:"timeout"`
	OnError  string `json:"on_error"`
}

func (h Hook) EffectivePriority() int {
	if h.Priority == nil {
		return 10
	}
	return *h.Priority
}

var hookName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
var bundleName = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validBundlePath(p string) bool {
	if !utf8.ValidString(p) || !bundleName.MatchString(p) || path.Clean(p) != p || strings.HasPrefix(p, "/") || p == "." {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.HasSuffix(part, ".") {
			return false
		}
	}
	// Refuse case-folding collisions separately in artifact validation.
	return true
}
func (m Manifest) validateV2() error {
	s := m.Server
	switch s.Runtime {
	case "node", "deno", "bun", "binary":
	default:
		return fmt.Errorf("server.runtime must be node, deno, bun or binary")
	}
	if !validBundlePath(s.Entry) || !strings.HasPrefix(s.Entry, "bin/") {
		return fmt.Errorf("server.entry must be a canonical bundled path under bin/")
	}
	if s.Runtime != "binary" && path.Ext(s.Entry) != ".js" && path.Ext(s.Entry) != ".mjs" && path.Ext(s.Entry) != ".cjs" {
		return fmt.Errorf("server.entry must be precompiled JavaScript")
	}
	if len(s.Engines) != 1 {
		return fmt.Errorf("server.engines must declare exactly the selected runtime")
	}
	r, ok := s.Engines[s.Runtime]
	if !ok {
		return fmt.Errorf("server.engines must declare selected runtime %s", s.Runtime)
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("server.engines.%s: %w", s.Runtime, err)
	}
	if m.UI != nil {
		if !validBundlePath(m.UI.Bundle) || !strings.HasPrefix(m.UI.Bundle, "ui/") || (path.Ext(m.UI.Bundle) != ".js" && path.Ext(m.UI.Bundle) != ".mjs") {
			return fmt.Errorf("ui.bundle must be bundled JavaScript under ui/")
		}
		if m.UI.Stylesheet != "" && (!validBundlePath(m.UI.Stylesheet) || !strings.HasPrefix(m.UI.Stylesheet, "ui/") || path.Ext(m.UI.Stylesheet) != ".css") {
			return fmt.Errorf("ui.stylesheet must be bundled CSS under ui/")
		}
		if m.UI.Isolation != "iframe" && m.UI.Isolation != "shared" {
			return fmt.Errorf("ui.isolation must be iframe or shared")
		}
	}
	seen := map[string]bool{}
	for i, h := range m.Hooks {
		if !hookName.MatchString(h.Name) || seen[h.Name] {
			return fmt.Errorf("hooks[%d].name must be canonical and unique", i)
		}
		seen[h.Name] = true
		switch h.Mode {
		case "sequential", "parallel", "bail", "waterfall", "async", "after_commit":
		default:
			return fmt.Errorf("hooks[%d].mode is invalid", i)
		}
		if h.Timeout <= 0 {
			return fmt.Errorf("hooks[%d].timeout must be positive milliseconds", i)
		}
		if h.OnError != "open" && h.OnError != "closed" {
			return fmt.Errorf("hooks[%d].on_error must be open or closed", i)
		}
	}
	if err := m.Artifact.Validate(); err != nil {
		return err
	}
	refs := []string{s.Entry}
	if m.UI != nil {
		refs = append(refs, m.UI.Bundle)
		if m.UI.Stylesheet != "" {
			refs = append(refs, m.UI.Stylesheet)
		}
	}
	for _, ref := range refs {
		found := false
		for _, f := range m.Artifact.Files {
			if f.Path == ref {
				found = true
				if f.Executable != (ref == s.Entry && s.Runtime == "binary") {
					return fmt.Errorf("artifact executable mode disagrees with %s", ref)
				}
			}
		}
		if !found {
			return fmt.Errorf("artifact.files must include %s", ref)
		}
	}
	return nil
}
