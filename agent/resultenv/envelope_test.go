package resultenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

type fixture struct {
	Name     string          `json:"name"`
	Envelope json.RawMessage `json:"envelope"`
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 15 {
		t.Fatalf("fixture count = %d, want 15", len(fixtures))
	}
	return fixtures
}

func refFor(raw []byte) runtimecontract.Reference {
	sum := sha256.Sum256(raw)
	return runtimecontract.Reference{
		ID: "receipt-1", Revision: "v1", IssuerRef: "test-host", OwnerID: "owner-1",
		Digest: runtimecontract.Digest{Scheme: "sha256", Value: hex.EncodeToString(sum[:])},
	}
}

func TestFixturesRoundTrip(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			var e Envelope
			if err := json.Unmarshal(f.Envelope, &e); err != nil {
				t.Fatal(err)
			}
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			r, err := NewReceipt(refFor(f.Envelope), f.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Envelope()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, f.Envelope) {
				t.Fatal("receipt changed envelope bytes")
			}
			if len(e.MCPResult) != 0 {
				projected, err := FromMCPRaw(e.MCPResult, e.OutputSchemaRef)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(projected.MCPResult, e.MCPResult) {
					t.Fatal("projection changed MCP bytes")
				}
				if projected.Outcome != e.Outcome {
					t.Fatalf("outcome = %s, want %s", projected.Outcome, e.Outcome)
				}
			}
			view, err := ModelView(r, 8192)
			if err != nil {
				t.Fatal(err)
			}
			if len(view) > 8192 {
				t.Fatal("unbounded view")
			}
			if !bytes.Contains(view, []byte(`"receipt_ref":"receipt-1"`)) {
				t.Fatal("model view lacks receipt reference")
			}
			if e.AutoRetryAllowed() {
				t.Fatal("automatic retry enabled")
			}
		})
	}
}

func TestRedactionAndBudget(t *testing.T) {
	var target fixture
	for _, f := range loadFixtures(t) {
		if f.Name == "metadata_annotations_redaction" {
			target = f
		}
	}
	r, err := NewReceipt(refFor(target.Envelope), target.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ModelView(r, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(view, []byte("fixture-hidden")) || bytes.Count(view, []byte("[REDACTED]")) != 2 {
		t.Fatalf("redaction failed: %s", view)
	}
	if !bytes.Contains(r.EnvelopeJSON, []byte("fixture-hidden")) {
		t.Fatal("receipt was redacted")
	}
	compact, err := ModelView(r, 90)
	if err != nil || len(compact) > 90 || !bytes.Contains(compact, []byte(`"truncated":true`)) {
		t.Fatalf("compact view = %s, err = %v", compact, err)
	}
	if _, err := ModelView(r, 5); err == nil {
		t.Fatal("tiny budget accepted")
	}
}

func TestUnknownDistinctFromNotRunAndError(t *testing.T) {
	seen := map[Outcome]bool{}
	for _, f := range loadFixtures(t) {
		if strings.Contains(f.Name, "unknown") || f.Name == "not_run" || f.Name == "tool_error" {
			var e Envelope
			if err := json.Unmarshal(f.Envelope, &e); err != nil {
				t.Fatal(err)
			}
			seen[e.Outcome] = true
		}
	}
	if len(seen) != 3 || !seen[Unknown] || !seen[NotRun] || !seen[ToolError] {
		t.Fatalf("outcomes collapsed: %v", seen)
	}
}

func TestDirectCommandOutcomes(t *testing.T) {
	want := map[string]Outcome{
		"direct_exec_success": Success,
		"pty_combined_output": Success,
		"nonzero_exit":        NonzeroExit,
		"signal_exit":         Signal,
		"direct_tool_error":   ToolError,
		"cancelled":           Cancelled,
	}
	for _, f := range loadFixtures(t) {
		outcome, ok := want[f.Name]
		if !ok {
			continue
		}
		var e Envelope
		if err := json.Unmarshal(f.Envelope, &e); err != nil {
			t.Fatal(err)
		}
		if e.Outcome != outcome {
			t.Fatalf("%s outcome = %s, want %s", f.Name, e.Outcome, outcome)
		}
		switch f.Name {
		case "direct_exec_success":
			if len(e.MCPResult) != 0 || e.ExitCode == nil || *e.ExitCode != 0 {
				t.Fatal("direct success fabricated MCP result or lost exit code")
			}
		case "signal_exit":
			if e.Signal != "SIGTERM" || e.ExitCode != nil {
				t.Fatal("signal collapsed into exit code")
			}
		case "cancelled":
			if e.CancelRequested == nil || !*e.CancelRequested || e.CancelConfirmed == nil || !*e.CancelConfirmed {
				t.Fatal("cancel request/confirmation lost")
			}
		case "pty_combined_output":
			if e.CombinedOutput == "" || e.Stdout != "" || e.Stderr != "" {
				t.Fatal("PTY stream was split or lost")
			}
		}
	}
}

func TestSerializedReceiptPreservesEnvelopeBytes(t *testing.T) {
	for _, f := range loadFixtures(t) {
		r, err := NewReceipt(refFor(f.Envelope), f.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var restored Receipt
		if err := json.Unmarshal(wire, &restored); err != nil {
			t.Fatal(err)
		}
		got, err := restored.Envelope()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, f.Envelope) {
			t.Fatalf("%s: receipt JSON changed envelope bytes", f.Name)
		}
	}
}

func TestModelViewPreservesLargeNumbers(t *testing.T) {
	raw := []byte(`{
  "version": "resultenv/v1",
  "outcome": "success",
  "mcp_result": {"content": [], "structuredContent": {"id": 9007199254740993, "decimal": 1.234567890123456789}}
}`)
	r, err := NewReceipt(refFor(raw), raw)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ModelView(r, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"9007199254740993", "1.234567890123456789"} {
		if !bytes.Contains(view, []byte(want)) {
			t.Fatalf("model view lost numeric precision for %s: %s", want, view)
		}
	}
}

func TestReceiptRejectsTampering(t *testing.T) {
	f := loadFixtures(t)[0]
	ref := refFor(f.Envelope)
	ref.Digest.Value = strings.Repeat("0", 64)
	if _, err := NewReceipt(ref, f.Envelope); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}
