package tesseracttest

import (
	"encoding/json"

	tesseract "github.com/hollis-labs/libs/util/tesseractclient"
)

// Opt adjusts a revision under construction.
type Opt func(*tesseract.Revision)

// NewRevision builds a canonical knowledge-domain revision at
// (namespace, key) with conforming defaults: a summary, a body, confidence
// 0.9 and a stable revision id "rev-<namespace>/<key>". Options then override.
func NewRevision(namespace, key string, opts ...Opt) tesseract.Revision {
	var rev tesseract.Revision
	rev.RevisionID = "rev-" + namespace + "/" + key
	rev.ItemID = "item-" + namespace + "/" + key
	rev.Domain = "knowledge"
	rev.Namespace = namespace
	rev.MemoryKey = key
	rev.Status = "canonical"
	rev.CreatedAt = "2026-09-01T00:00:00Z"
	rev.Confidence = 0.9
	rev.Tags = []string{}
	rev.Payload.Summary = "Summary of " + key
	rev.Payload.Body = "Body of " + key
	rev.Author.AgentID = "test"
	rev.SessionID = "session-test"
	rev.Facets.Kind = "note"
	rev.Facets.Source = "manual"
	for _, o := range opts {
		o(&rev)
	}
	return rev
}

// RevisionID sets the revision id.
func RevisionID(id string) Opt { return func(r *tesseract.Revision) { r.RevisionID = id } }

// Status sets the status, e.g. "deprecated".
func Status(s string) Opt { return func(r *tesseract.Revision) { r.Status = s } }

// Summary sets the summary.
func Summary(s string) Opt { return func(r *tesseract.Revision) { r.Payload.Summary = s } }

// Body sets the body.
func Body(s string) Opt { return func(r *tesseract.Revision) { r.Payload.Body = s } }

// Tags replaces the tag list.
func Tags(tags ...string) Opt { return func(r *tesseract.Revision) { r.Tags = tags } }

// Created sets created_at.
func Created(ts string) Opt { return func(r *tesseract.Revision) { r.CreatedAt = ts } }

// Data merges keys into payload.data.
func Data(kv map[string]any) Opt {
	return func(r *tesseract.Revision) { r.Payload.Data = merge(r.Payload.Data, kv) }
}

// State merges keys into consumer_state.
func State(kv map[string]any) Opt {
	return func(r *tesseract.Revision) { r.ConsumerState = merge(r.ConsumerState, kv) }
}

func merge(existing json.RawMessage, kv map[string]any) json.RawMessage {
	m := map[string]any{}
	_ = json.Unmarshal(existing, &m)
	for k, v := range kv {
		m[k] = v
	}
	out, _ := json.Marshal(m)
	return out
}
