package pluginhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Kind distinguishes observations from transformations.
type Kind string

const (
	Action Kind = "action"
	Filter Kind = "filter"
)

// Mode declares execution semantics. Registry-only builds do not execute modes.
type Mode string

const (
	Sequential  Mode = "sequential"
	Parallel    Mode = "parallel"
	Bail        Mode = "bail"
	Waterfall   Mode = "waterfall"
	Async       Mode = "async"
	AfterCommit Mode = "after_commit"
)

// ErrorPolicy specifies how an ordinary handler failure affects a dispatch.
type ErrorPolicy string

const (
	Open   ErrorPolicy = "open"
	Closed ErrorPolicy = "closed"
)

var (
	ErrInvalidDefinition = errors.New("invalid hook definition")
	ErrUnknownHook       = errors.New("unknown hook")
	ErrUnauthorized      = errors.New("unauthorized registration")
	ErrInvalidOptions    = errors.New("invalid registration options")
	ErrDuplicate         = errors.New("duplicate registration or generation")
	ErrDisposed          = errors.New("scope disposed")
	ErrUnavailable       = errors.New("handler unavailable")
)

// Validator is a host-compiled schema validator. It must be concurrency-safe.
// It must not retain or mutate its input. This package performs no schema compilation.
type Validator func(json.RawMessage) error

// Deprecation describes a declaration's removal plan; it never aliases names.
type Deprecation struct{ Since, Replacement, Reason, Removal string }

// Definition is an explicit host policy, with no runtime presets.
// RemoteOK is a pointer so omission can be rejected distinctly from false.
type Definition struct {
	Name                                         string
	Kind                                         Kind
	Mode                                         Mode
	InputSchema, OutputSchema                    json.RawMessage
	ValidateInput, ValidateOutput                Validator
	MutablePaths                                 []string
	Since                                        string
	Deprecated                                   *Deprecation
	RemoteOK                                     *bool
	Budget, HandlerTimeout                       time.Duration
	OnErrorDefault                               ErrorPolicy
	AllowedOnError                               []ErrorPolicy
	MaxPayloadBytes, MaxHandlers, MaxParallelism int
	Views                                        map[string][]string
	RequiredView                                 string
	SchemaDigest                                 string
}

// Catalog is a versioned collection of host declarations, copied on installation.
type Catalog struct {
	Version     string
	Definitions []Definition
}

var hookName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func validPointer(p string) bool {
	if p == "" {
		return true
	}
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '~' {
			i++
			if i == len(p) || (p[i] != '0' && p[i] != '1') {
				return false
			}
		}
	}
	return true
}
func validateDefinition(d Definition) error {
	bad := func(s string) error { return fmt.Errorf("%w: %s: %s", ErrInvalidDefinition, d.Name, s) }
	if !hookName.MatchString(d.Name) || d.Since == "" || d.SchemaDigest == "" {
		return bad("name, since and schema digest required")
	}
	if d.RemoteOK == nil || d.Budget <= 0 || d.HandlerTimeout <= 0 || d.HandlerTimeout > d.Budget || d.MaxPayloadBytes <= 0 || d.MaxHandlers <= 0 || d.MaxParallelism <= 0 {
		return bad("explicit finite policy limits required")
	}
	validSchema := func(s json.RawMessage) bool {
		var v map[string]json.RawMessage
		return json.Unmarshal(s, &v) == nil && v != nil
	}
	if !validSchema(d.InputSchema) || d.ValidateInput == nil {
		return bad("input schema and compiled validator required")
	}
	switch d.Kind {
	case Action:
		if len(d.OutputSchema) != 0 || d.ValidateOutput != nil || len(d.MutablePaths) != 0 {
			return bad("action output/mutation contract forbidden")
		}
		switch d.Mode {
		case Sequential, Parallel, Bail, Async, AfterCommit:
		default:
			return bad("invalid action mode")
		}
	case Filter:
		if d.Mode != Waterfall || !validSchema(d.OutputSchema) || d.ValidateOutput == nil {
			return bad("filter requires waterfall and output validator")
		}
	default:
		return bad("unknown kind")
	}
	if d.OnErrorDefault != Open && d.OnErrorDefault != Closed {
		return bad("explicit error policy required")
	}
	if !slices.Contains(d.AllowedOnError, d.OnErrorDefault) {
		return bad("default error policy must be allowed")
	}
	for _, p := range d.AllowedOnError {
		if p != Open && p != Closed {
			return bad("unknown error policy")
		}
	}
	for _, p := range d.MutablePaths {
		if !validPointer(p) {
			return bad("invalid mutable JSON pointer")
		}
	}
	for name, paths := range d.Views {
		if name == "" {
			return bad("empty view name")
		}
		for _, p := range paths {
			if !validPointer(p) {
				return bad("invalid view JSON pointer")
			}
		}
	}
	if d.RequiredView != "" {
		if _, ok := d.Views[d.RequiredView]; !ok {
			return bad("required view unavailable")
		}
	}
	return nil
}
func copyDefinition(d Definition) Definition {
	d.InputSchema = slices.Clone(d.InputSchema)
	d.OutputSchema = slices.Clone(d.OutputSchema)
	d.MutablePaths = slices.Clone(d.MutablePaths)
	d.AllowedOnError = slices.Clone(d.AllowedOnError)
	if d.RemoteOK != nil {
		v := *d.RemoteOK
		d.RemoteOK = &v
	}
	if d.Deprecated != nil {
		v := *d.Deprecated
		d.Deprecated = &v
	}
	if d.Views != nil {
		v := make(map[string][]string, len(d.Views))
		for k, p := range d.Views {
			v[k] = slices.Clone(p)
		}
		d.Views = v
	}
	return d
}
