package chatstream

import (
	"errors"
	"testing"
)

func TestCapabilitiesValidate(t *testing.T) {
	ok := Capabilities{Framing: FramingSSE, Text: GranularityToken, Tools: true, ToolArgs: GranularityToken,
		Reasoning: ReasoningFull, ReasoningRoundTrip: true, Usage: UsageIncremental, CacheTokens: true, ReasoningTokens: true}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid capabilities rejected: %v", err)
	}
	// a dialect with tools but no streamed arguments (OpenCode) and one with
	// reasoning tokens but no reasoning stream (OpenAI Chat) are both legitimate
	for name, c := range map[string]Capabilities{
		"tools without streamed args":      {Framing: FramingNDJSON, Text: GranularityChunk, Tools: true},
		"reasoning tokens, no reasoning":   {Framing: FramingSSE, Text: GranularityToken, Usage: UsageAtEnd, ReasoningTokens: true},
		"nothing but framing":              {Framing: FramingJSONRPCLines},
		"whole-message text, cli":          {Framing: FramingNDJSON, Text: GranularityFinal, Tools: true, ToolArgs: GranularityFinal},
		"approval only in band, cancel ok": {Framing: FramingJSONRPCLines, Approval: ApprovalCapInBand, Cancel: true},
	} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCapabilitiesValidateRejectsContradictions(t *testing.T) {
	bad := map[string]Capabilities{
		"unset framing":                   {},
		"tool args streamed but no tools": {Framing: FramingSSE, ToolArgs: GranularityToken},
		"round trip without reasoning":    {Framing: FramingSSE, ReasoningRoundTrip: true},
		"cache tokens without usage":      {Framing: FramingSSE, CacheTokens: true},
		"reasoning tokens without usage":  {Framing: FramingSSE, ReasoningTokens: true},
	}
	for name, c := range bad {
		if err := c.Validate(); !errors.Is(err, ErrInvalidCapabilities) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestVerbAndKindVocabularies(t *testing.T) {
	for _, v := range []Verb{VerbRunFinish, VerbRunError, VerbRunAbort} {
		if !v.Terminal() {
			t.Errorf("%s must be terminal", v)
		}
	}
	for _, v := range []Verb{VerbRunStart, VerbPartEnd, VerbUsage, VerbGap, VerbRaw, VerbStepFinish} {
		if v.Terminal() {
			t.Errorf("%s must not be terminal", v)
		}
	}
	if Verb("x.y").Known() || !VerbApprovalRequest.Known() {
		t.Error("Known is wrong")
	}
	if !PartText.Streamed() || !PartToolCall.Streamed() || PartToolResult.Streamed() || PartFile.Streamed() {
		t.Error("Streamed is wrong")
	}
	if FinishReason("bogus").Known() || !FinishPause.Known() {
		t.Error("FinishReason.Known is wrong")
	}
}
