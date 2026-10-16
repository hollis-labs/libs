package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/ledger"
	"github.com/hollis-labs/libs/core/util/ledger/ledgertest"
)

func TestMemStoreConforms(t *testing.T) {
	ledgertest.Run(t, func() ledger.Store { return &ledger.MemStore{} })
}

func TestKeyIsPerSubjectStageVersion(t *testing.T) {
	v := ledger.Version{Label: "v1", Hash: "h"}
	k := ledger.Key("s", ledger.StageRule, v)
	if k != ledger.Key("s", ledger.StageRule, v) {
		t.Fatal("not deterministic")
	}
	for name, other := range map[string]string{
		"subject": ledger.Key("s2", ledger.StageRule, v),
		"stage":   ledger.Key("s", ledger.StageClassifier, v),
		"label":   ledger.Key("s", ledger.StageRule, ledger.Version{Label: "v2", Hash: "h"}),
		"hash":    ledger.Key("s", ledger.StageRule, ledger.Version{Label: "v1", Hash: "h2"}),
		// length-prefixing: these two must not collide
		"boundary": ledger.Key("s|rule", ledger.Stage(""), v),
	} {
		if other == k {
			t.Errorf("%s change did not change the key", name)
		}
	}
}

func TestRetentionShape(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw := json.RawMessage(`{"q":"my card is 4111"}`)

	ret := ledger.DefaultRetention()
	in, err := ret.Shape(ledger.StageClassifier, raw, "", false, now)
	if err != nil || in.Mode != ledger.ModeSnapshot || in.RawExpiresAt == nil || !in.RawExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("%+v %v", in, err)
	}
	keep, _ := ret.Shape(ledger.StageClassifier, raw, "", true, now)
	if keep.RawExpiresAt != nil || !keep.Keep {
		t.Fatalf("keep flag must stop expiry: %+v", keep)
	}

	// per-stage override: redaction + none for another stage
	redact := func(b []byte) ([]byte, error) {
		return []byte(strings.ReplaceAll(string(b), "4111", "[redacted]")), nil
	}
	ret.Stages = map[ledger.Stage]ledger.StageRetention{
		ledger.StageHuman:  {Mode: ledger.ModeNone},
		ledger.StageModel:  {Mode: ledger.ModePointer},
		ledger.StageReflex: {Mode: ledger.ModeSnapshot, RawTTL: time.Hour, Redact: redact},
	}
	if in, _ := ret.Shape(ledger.StageHuman, raw, "", false, now); in.Mode != ledger.ModeNone || in.Snapshot != nil || in.Hash == "" {
		t.Fatalf("none keeps the hash only: %+v", in)
	}
	if _, err := ret.Shape(ledger.StageModel, raw, "", false, now); err == nil {
		t.Fatal("pointer mode without a pointer must error")
	}
	if in, _ := ret.Shape(ledger.StageModel, raw, "cas://sha256/x", false, now); in.Pointer != "cas://sha256/x" || in.Snapshot != nil {
		t.Fatalf("%+v", in)
	}
	in, _ = ret.Shape(ledger.StageReflex, raw, "", false, now)
	if strings.Contains(string(in.Snapshot), "4111") || !in.Redacted {
		t.Fatalf("redaction not applied: %+v", in)
	}
	ret.Stages[ledger.StageReflex] = ledger.StageRetention{Mode: ledger.ModeSnapshot, Redact: func([]byte) ([]byte, error) { return nil, errors.New("boom") }}
	if _, err := ret.Shape(ledger.StageReflex, raw, "", false, now); err == nil {
		t.Fatal("a failing redactor must refuse the write, not store raw")
	}
}

func TestSamplingDowngradesToPointerOrNone(t *testing.T) {
	half := 0.5
	ret := ledger.Retention{Default: ledger.StageRetention{Mode: ledger.ModeSnapshot, Sample: &half}, Rand: func() float64 { return 0.9 }}
	raw := json.RawMessage(`{}`)
	if in, _ := ret.Shape(ledger.StageRule, raw, "ptr://1", false, time.Now()); in.Mode != ledger.ModePointer {
		t.Fatalf("%+v", in)
	}
	if in, _ := ret.Shape(ledger.StageRule, raw, "", false, time.Now()); in.Mode != ledger.ModeNone || in.Hash == "" {
		t.Fatalf("%+v", in)
	}
	ret.Rand = func() float64 { return 0.1 }
	if in, _ := ret.Shape(ledger.StageRule, raw, "", false, time.Now()); in.Mode != ledger.ModeSnapshot {
		t.Fatalf("%+v", in)
	}
	if in, _ := ret.Shape(ledger.StageRule, raw, "", true, time.Now()); in.Mode != ledger.ModeSnapshot {
		t.Fatal("keep rows are never sampled out")
	}
}

func TestExpireKeepsEverythingButTheSnapshot(t *testing.T) {
	ctx := context.Background()
	s := &ledger.MemStore{}
	now := time.Unix(1_800_000_000, 0)
	mk := func(subject string, keep bool) ledger.Record {
		r := ledgertest.Record(subject, ledger.StageClassifier, "q")
		in, _ := ledger.DefaultRetention().Shape(ledger.StageClassifier, json.RawMessage(`{"x":1}`), "", keep, now)
		r.Input = in
		return r
	}
	a, _, _ := s.Append(ctx, mk("old", false))
	k, _, _ := s.Append(ctx, mk("training", true))

	if n, _ := s.ExpireInputs(ctx, now.Add(29*24*time.Hour)); n != 0 {
		t.Fatalf("expired early: %d", n)
	}
	if n, _ := s.ExpireInputs(ctx, now.Add(31*24*time.Hour)); n != 1 {
		t.Fatalf("expired %d", n)
	}
	ea, _ := s.Get(ctx, a.DecisionID)
	if ea.Record.Input.Snapshot != nil || !ea.Record.Input.Expired || ea.Record.Input.Hash == "" || ea.Record.Output.Label != "q" {
		t.Fatalf("%+v", ea.Record)
	}
	ek, _ := s.Get(ctx, k.DecisionID)
	if ek.Record.Input.Snapshot == nil {
		t.Fatal("keep row lost its snapshot")
	}
}

func TestHashKeyMakesHMAC(t *testing.T) {
	if ledger.HashInput([]byte("a@b.c"), nil) == ledger.HashInput([]byte("a@b.c"), []byte("k")) {
		t.Fatal("key ignored")
	}
}
