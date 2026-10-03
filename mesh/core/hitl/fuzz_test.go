package hitl_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/hollis-labs/substrate/mesh/hitl"
	"github.com/hollis-labs/substrate/mesh/hitl/hitltest"
)

func FuzzUnmarshalOutcome(f *testing.F) {
	for _, fx := range hitltest.Fixtures() {
		if fx.Valid {
			f.Add(fx.Doc)
		}
	}
	f.Add([]byte(`{"state":"expired","item_id":"i","interaction_revision":1,"terminated_at":"2026-01-01T00:00:00Z"}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		o, err := hitl.UnmarshalOutcome(data)
		if err != nil {
			return
		}
		if !o.OutcomeState().IsTerminal() || o.OutcomeItemID() == "" || o.OutcomeRevision() < 1 {
			t.Fatalf("accepted an inconsistent outcome: %#v", o)
		}
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatalf("accepted outcome does not marshal: %v", err)
		}
		again, err := hitl.UnmarshalOutcome(b)
		if err != nil {
			t.Fatalf("re-marshaled outcome rejected: %v\n%s", err, b)
		}
		b2, err := json.Marshal(again)
		if err != nil || !bytes.Equal(b, b2) {
			t.Fatalf("not stable across a round trip:\n %s\n %s (%v)", b, b2, err)
		}
	})
}

func FuzzDecodeError(f *testing.F) {
	for _, fx := range hitltest.Fixtures() {
		if fx.Valid {
			f.Add(fx.Doc)
		}
	}
	f.Add([]byte(`{"code":"not_found"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		err := hitl.DecodeError(data)
		if err == nil {
			t.Fatal("DecodeError must always return an error")
		}
		// Whatever it decoded to must re-encode to something it decodes again.
		if back := hitl.DecodeError(hitl.EncodeError(err)); back == nil {
			t.Fatal("re-encoded error decoded to nil")
		}
	})
}
