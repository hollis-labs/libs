package modelsdev

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnknownCapabilities marks absent flags. Manually constructed Capabilities
// retain the existing convention that a false flag means unsupported.
type UnknownCapabilities struct {
	ToolCall         bool `json:"tool_call,omitempty"`
	Reasoning        bool `json:"reasoning,omitempty"`
	Attachment       bool `json:"attachment,omitempty"`
	Temperature      bool `json:"temperature,omitempty"`
	StructuredOutput bool `json:"structured_output,omitempty"`
}

// ModelProvider describes upstream request metadata, not trusted configuration.
// Applications must review it before using endpoints, bodies or headers.
type ModelProvider struct {
	NPM     string                     `json:"npm,omitempty"`
	API     string                     `json:"api,omitempty"`
	Shape   string                     `json:"shape,omitempty"`
	Body    map[string]json.RawMessage `json:"body,omitempty"`
	Headers map[string]string          `json:"headers,omitempty"`
}

// ReasoningOption is an upstream toggle, effort or budget_tokens control.
// A nil element in Values preserves a JSON null effort; nil Min/Max means absent.
// Type remains a string so future upstream control types can be represented.
type ReasoningOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values,omitzero"`
	Min    *int      `json:"min,omitempty"`
	Max    *int      `json:"max,omitempty"`
}

// Interleaved represents upstream true or an object naming the reasoning field.
// A nil *Interleaved means the upstream field was absent.
type Interleaved struct{ Field string }

func (i *Interleaved) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("true")) {
		*i = Interleaved{}
		return nil
	}
	var obj struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	if obj.Field == "" {
		return fmt.Errorf("modelsdev: interleaved requires true or a field")
	}
	i.Field = obj.Field
	return nil
}

func (i Interleaved) MarshalJSON() ([]byte, error) {
	if i.Field == "" {
		return []byte("true"), nil
	}
	return json.Marshal(struct {
		Field string `json:"field"`
	}{i.Field})
}

// Experimental preserves named upstream modes and their request/price overrides.
type Experimental struct {
	Modes map[string]ExperimentalMode `json:"modes,omitempty"`
}

// ExperimentalMode describes one mode without applying its overrides.
type ExperimentalMode struct {
	Cost     *Pricing       `json:"cost,omitempty"`
	Provider *ModelProvider `json:"provider,omitempty"`
}
