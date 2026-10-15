package chatstream

import (
	"errors"
	"fmt"
)

// Granularity is how finely a dialect streams something.
type Granularity uint8

// The granularities. The zero value means the dialect does not stream the
// thing at all.
const (
	GranularityNone Granularity = iota
	// GranularityToken: many small deltas.
	GranularityToken
	// GranularityChunk: a few larger deltas (whole sentences, whole blocks).
	GranularityChunk
	// GranularityFinal: the whole value once, at the end.
	GranularityFinal
)

func (g Granularity) String() string {
	switch g {
	case GranularityNone:
		return "none"
	case GranularityToken:
		return "token"
	case GranularityChunk:
		return "chunk"
	case GranularityFinal:
		return "final"
	}
	return fmt.Sprintf("Granularity(%d)", uint8(g))
}

// ReasoningCap is how much reasoning a dialect exposes.
type ReasoningCap uint8

// The reasoning capabilities.
const (
	ReasoningNone ReasoningCap = iota
	// ReasoningSummary: a summary of the reasoning.
	ReasoningSummary
	// ReasoningFull: the reasoning text itself.
	ReasoningFull
)

// UsageTiming is when a dialect reports usage.
type UsageTiming uint8

// The usage timings.
const (
	UsageNone UsageTiming = iota
	// UsageAtEnd: once, with or after the last content.
	UsageAtEnd
	// UsageIncremental: at the start and again as the run proceeds.
	UsageIncremental
)

// ApprovalCap is which approval modes a dialect supports. It is a plain
// enumeration; the wire value of a single request is ApprovalMode.
type ApprovalCap uint8

// The approval capabilities.
const (
	ApprovalNone ApprovalCap = iota
	ApprovalCapInBand
	ApprovalCapSuspend
)

// Framing is how a dialect's stream is cut into frames.
type Framing uint8

// The framings. The zero value is unset.
const (
	FramingUnset Framing = iota
	// FramingSSE: server-sent events; a Frame carries Event and Data.
	FramingSSE
	// FramingNDJSON: one JSON value per line; a Frame carries Data.
	FramingNDJSON
	// FramingJSONRPCLines: newline-delimited JSON-RPC 2.0 messages (ACP over
	// stdio); a Frame carries Data.
	FramingJSONRPCLines
)

// Capabilities declares what a provider's stream can and cannot do. It is
// static and immutable, and safe for concurrent use because it is a value:
// consumers adapt to it, and an adapter never fakes a capability (no invented
// token-level streaming). The zero value declares nothing.
type Capabilities struct {
	// Text and ToolArgs are how finely text and tool-call arguments stream.
	Text, ToolArgs Granularity
	// Tools: the dialect carries tool calls at all.
	Tools bool
	// Reasoning is how much reasoning is exposed; ReasoningRoundTrip says a
	// signature or encrypted value must be sent back with the next request.
	Reasoning          ReasoningCap
	ReasoningRoundTrip bool
	// Usage is when usage arrives. CacheTokens and ReasoningTokens say it
	// breaks those out.
	Usage                        UsageTiming
	CacheTokens, ReasoningTokens bool
	// Citations, Refusal, ServerTools, Files: the dialect has parts of these
	// kinds. InterleavedParts: parts may be open at the same time.
	Citations, Refusal, ServerTools, Files, InterleavedParts bool
	// Cancel: the upstream can be canceled mid-stream. ResumeCursor: it can
	// resume from a cursor.
	Cancel, ResumeCursor bool
	// Approval is which approval modes the dialect supports.
	Approval ApprovalCap
	// Framing is how the stream is cut into frames.
	Framing Framing
}

// ErrInvalidCapabilities is what Capabilities.Validate returns.
var ErrInvalidCapabilities = errors.New("chatstream: invalid capabilities")

// Validate rejects a declaration that contradicts itself: something is
// claimed about a thing the dialect does not have. Registries call it once, at
// registration.
func (c Capabilities) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidCapabilities, fmt.Sprintf(format, args...))
	}
	switch {
	case c.Framing == FramingUnset:
		return bad("framing is unset")
	case c.ToolArgs != GranularityNone && !c.Tools:
		return bad("tool arguments stream (%s) but Tools is false", c.ToolArgs)
	case c.ReasoningRoundTrip && c.Reasoning == ReasoningNone:
		return bad("ReasoningRoundTrip without Reasoning")
	case (c.CacheTokens || c.ReasoningTokens) && c.Usage == UsageNone:
		return bad("cache or reasoning token counts without any usage")
	}
	return nil
}
