package modelsdev

import "encoding/json"

// Catalog is the top-level result returned by the models.dev API.
type Catalog struct {
	Providers map[string]Provider
}

// Provider describes a single LLM provider and its available models.
//
// Env carries one or more environment-variable names the provider expects
// callers to set (e.g. ["ANTHROPIC_API_KEY"]). The API can return an empty
// list for providers that don't require a key.
type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Env    []string         `json:"env"`
	Doc    string           `json:"doc,omitempty"`
	NPM    string           `json:"npm,omitempty"`
	API    string           `json:"api,omitempty"`
	Models map[string]Model `json:"models"`
}

// Model describes a single LLM model within a provider.
//
// The models.dev API encodes capability flags (tool_call, reasoning,
// attachment, temperature, structured_output) as top-level booleans;
// this type folds them into the nested Capabilities struct for ergonomic
// access in Go. Modality is decoded from the API field "modalities".
type Model struct {
	Description      string            `json:"description,omitempty"`
	CanonicalModelID string            `json:"canonical_model_id,omitempty"`
	Type             string            `json:"type,omitempty"`
	Status           string            `json:"status,omitempty"`
	Provider         *ModelProvider    `json:"provider,omitempty"`
	ReasoningOptions []ReasoningOption `json:"reasoning_options,omitzero"`
	Interleaved      *Interleaved      `json:"interleaved,omitempty"`
	Experimental     *Experimental     `json:"experimental,omitempty"`

	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Family          string       `json:"family"`
	OpenWeights     bool         `json:"open_weights"`
	ReleaseDate     string       `json:"release_date"`
	KnowledgeCutoff string       `json:"knowledge"`
	LastUpdated     string       `json:"last_updated"`
	Cost            Pricing      `json:"cost"`
	Limit           Limits       `json:"limit"`
	Modality        Modality     `json:"-"`
	Capabilities    Capabilities `json:"-"`
}

// modelWire keeps the upstream flat flags while Model exposes grouped capabilities.
// The alias avoids recursively invoking Model's JSON methods.
type modelAlias Model
type modelWire struct {
	*modelAlias
	Modalities       Modality `json:"modalities"`
	ToolCall         *bool    `json:"tool_call,omitempty"`
	Reasoning        *bool    `json:"reasoning,omitempty"`
	Attachment       *bool    `json:"attachment,omitempty"`
	Temperature      *bool    `json:"temperature,omitempty"`
	StructuredOutput *bool    `json:"structured_output,omitempty"`
}

func (m *Model) UnmarshalJSON(b []byte) error {
	// A missing cost object is unknown too, not a manually constructed free price.
	next := Model{Cost: UnknownPricing()}
	j := modelWire{modelAlias: (*modelAlias)(&next)}
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	next.Modality = j.Modalities
	set := func(src *bool, dst *bool, unknown *bool) {
		*unknown = src == nil
		if src != nil {
			*dst = *src
		}
	}
	set(j.ToolCall, &next.Capabilities.ToolCall, &next.Capabilities.Unknown.ToolCall)
	set(j.Reasoning, &next.Capabilities.Reasoning, &next.Capabilities.Unknown.Reasoning)
	set(j.Attachment, &next.Capabilities.Attachment, &next.Capabilities.Unknown.Attachment)
	set(j.Temperature, &next.Capabilities.Temperature, &next.Capabilities.Unknown.Temperature)
	set(j.StructuredOutput, &next.Capabilities.StructuredOutput, &next.Capabilities.Unknown.StructuredOutput)
	*m = next
	return nil
}

func (m Model) MarshalJSON() ([]byte, error) {
	flag := func(v bool, unknown bool) *bool {
		if unknown {
			return nil
		}
		return &v
	}
	c := m.Capabilities
	return json.Marshal(modelWire{
		modelAlias: (*modelAlias)(&m), Modalities: m.Modality,
		ToolCall:         flag(c.ToolCall, c.Unknown.ToolCall),
		Reasoning:        flag(c.Reasoning, c.Unknown.Reasoning),
		Attachment:       flag(c.Attachment, c.Unknown.Attachment),
		Temperature:      flag(c.Temperature, c.Unknown.Temperature),
		StructuredOutput: flag(c.StructuredOutput, c.Unknown.StructuredOutput),
	})
}

// Limits holds the token-count constraints for a model.
type Limits struct {
	ContextWindow   int  `json:"context"`
	MaxOutputTokens int  `json:"output"`
	MaxInputTokens  *int `json:"input,omitempty"`
}

// Modality describes which input and output modalities the model supports.
type Modality struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Capabilities describes the optional features a model supports. Decoded
// from flat top-level booleans on the API model object.
type Capabilities struct {
	ToolCall         bool `json:"tool_call"`
	Reasoning        bool `json:"reasoning"`
	Attachment       bool `json:"attachment"`
	Temperature      bool `json:"temperature"`
	StructuredOutput bool `json:"structured_output"`
	// Unknown distinguishes absent flags from explicitly unsupported capabilities.
	Unknown UnknownCapabilities `json:"unknown,omitempty"`
}

// ModelRef is a flat view of a Model that also carries its parent ProviderID.
// It is convenient when iterating all models across all providers.
type ModelRef struct {
	Description      string
	CanonicalModelID string
	Type             string
	Status           string
	Provider         *ModelProvider
	ReasoningOptions []ReasoningOption
	Interleaved      *Interleaved
	Experimental     *Experimental

	ProviderID string

	ID              string
	Name            string
	Family          string
	OpenWeights     bool
	ReleaseDate     string
	KnowledgeCutoff string
	LastUpdated     string
	Cost            Pricing
	Limit           Limits
	Modality        Modality
	Capabilities    Capabilities
}
