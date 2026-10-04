package subprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validateTranscriptMetadata(f transcript) error {
	if f.Status != "" && f.Status != "observed" && f.Status != "proposed" {
		return errors.New("invalid corpus status")
	}
	if f.Steps == nil {
		return errors.New("missing corpus steps")
	}
	if f.Level != "normative" && f.Level != "observed-quirk" {
		return errors.New("invalid corpus level")
	}
	if f.Status == "proposed" && (strings.TrimSpace(f.Finding) == "" || strings.TrimSpace(f.UnavailableOwner) == "") {
		return errors.New("proposed case needs finding and unavailable_owner")
	}
	for _, s := range f.Steps {
		level := s.Level
		if level == "" {
			level = f.Level
		}
		if level != "normative" && level != "observed-quirk" {
			return errors.New("invalid step level")
		}
		preferred := s.Preferred
		if preferred == "" {
			preferred = f.Preferred
		}
		if level == "observed-quirk" && strings.TrimSpace(preferred) == "" {
			return errors.New("quirk needs preferred note")
		}
	}
	return nil
}
func TestCorpusMetadataRejectsMissingObligationAndWaiver(t *testing.T) {
	for _, f := range []transcript{{}, {Level: "normative", Steps: []transcriptStep{}, Status: "other"}, {Level: "normative", Steps: []transcriptStep{}, Status: "proposed", Finding: "unavailable"}, {Level: "normative", Steps: []transcriptStep{{Level: "other"}}}, {Level: "normative", Steps: []transcriptStep{{Level: "observed-quirk"}}}} {
		if validateTranscriptMetadata(f) == nil {
			t.Fatalf("invalid metadata accepted: %+v", f)
		}
	}
	for _, f := range []transcript{{Level: "normative", Steps: []transcriptStep{}}, {Level: "normative", Steps: []transcriptStep{}, Status: "proposed", Finding: "unavailable", UnavailableOwner: "fixture owner"}, {Level: "normative", Steps: []transcriptStep{{Level: "observed-quirk", Preferred: "reject"}}}} {
		if err := validateTranscriptMetadata(f); err != nil {
			t.Fatal(err)
		}
	}
}

func (s *transcriptStep) UnmarshalJSON(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("fixture step must be an object")
	}
	type plain transcriptStep
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*s = transcriptStep(value)
	return nil
}

func TestCorpusMetadataRejectsNullStep(t *testing.T) {
	var f transcript
	if err := json.Unmarshal([]byte(`{"level":"normative","steps":[null]}`), &f); err == nil {
		t.Fatal("null step accepted")
	}
}
