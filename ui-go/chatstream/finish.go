package chatstream

// FinishReason is why a run finished, in one closed vocabulary. The
// dialect's own word travels beside it as Event.RawReason, so nothing is lost
// by mapping.
type FinishReason string

// The finish reasons.
const (
	// FinishStop: the model ended its turn normally.
	FinishStop FinishReason = "stop"
	// FinishLength: the output limit was reached.
	FinishLength FinishReason = "length"
	// FinishToolCalls: the model stopped to have tools run.
	FinishToolCalls FinishReason = "tool_calls"
	// FinishRefusal: the model declined.
	FinishRefusal FinishReason = "refusal"
	// FinishContentFilter: a provider content filter stopped the output.
	FinishContentFilter FinishReason = "content_filter"
	// FinishPause: the provider paused a long turn and expects a continuation.
	FinishPause FinishReason = "pause"
	// FinishContextExceeded: the context window was exhausted.
	FinishContextExceeded FinishReason = "context_exceeded"
	// FinishTurnLimit: an agent turn or request limit was reached.
	FinishTurnLimit FinishReason = "turn_limit"
	// FinishCancelled: the turn was canceled but the dialect reports it as a
	// finish rather than as an abort.
	FinishCancelled FinishReason = "cancelled" //nolint:misspell // the closed vocabulary's value, as ACP and AG-UI spell it
	// FinishError: the run ended because of an error the dialect reports as a
	// finish reason.
	FinishError FinishReason = "error"
	// FinishOther: a reason with no closed-vocabulary equivalent; see RawReason.
	FinishOther FinishReason = "other"
)

// Known reports whether r is one of the reasons above.
func (r FinishReason) Known() bool {
	switch r {
	case FinishStop, FinishLength, FinishToolCalls, FinishRefusal, FinishContentFilter,
		FinishPause, FinishContextExceeded, FinishTurnLimit, FinishCancelled, FinishError, FinishOther:
		return true
	}
	return false
}

// Run error codes a decoder or hub synthesizes. A dialect's own error codes
// pass through as run.error Code and are not limited to these.
const (
	// CodeUpstreamTruncated: the upstream ended (EOF, connection closed) with no
	// terminal signal. Decoders emit this, never a success, and mark it
	// retryable.
	CodeUpstreamTruncated = "upstream_truncated"
	// CodeStreamLost: the producer went away without publishing a terminal
	// event (the hub's Close-without-terminal case).
	CodeStreamLost = "stream_lost"
	// CodeMalformedFrame: a frame could not be decoded and the dialect gives no
	// way to continue.
	CodeMalformedFrame = "malformed_frame"
	// CodeFrameTooLarge: one frame (an SSE event or a line) was over the framing
	// layer's size limit. Not retryable, unlike truncation: a retry meets the same
	// frame. Raise the limit (framing.WithMaxEventBytes, framing.Lines' maxLine).
	CodeFrameTooLarge = "frame_too_large"
	// CodeUpstreamError: the upstream reported an error event.
	CodeUpstreamError = "upstream_error"
)

// Gap reasons for VerbGap events.
const (
	GapRetention      = "retention"
	GapCursorAhead    = "cursor_ahead"
	GapDroppedSlow    = "dropped_slow"
	GapIngestOverflow = "ingest_overflow"
)
