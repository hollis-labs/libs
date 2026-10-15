// Package schema embeds the go-hitl JSON Schema (draft 2020-12) bundle and
// offers a small per-definition validator over it.
//
// The bundle (lifecycle.schema.json) holds the generic lifecycle definitions
// derived from Tangent's tangent.hitl-item v1.0 contract: twelve definitions
// copied unchanged under Tangent's names, relaxed "Core" subsets of the
// presentation-bearing outputs, profile-tagged response kinds
// (x-hitl-tier: profile) and a few definitions new to go-hitl
// (x-hitl-origin: go-hitl), notably the terminal-conflict error and the
// participant proof slot.
//
// The bundle has no root validation: pick a definition with NewValidator.
// Format assertions (date-time) are enabled.
package schema
