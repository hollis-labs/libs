package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	plugin "github.com/hollis-labs/plugin-sdk"
)

// These fixtures exercise the real reader, dispatcher and writer. The fixture
// plugin is deliberately deterministic and has the same recipe for every SDK.
type transcript struct {
	Status      string           `json:"status,omitempty"`
	Finding     string           `json:"finding,omitempty"`
	Profile     string           `json:"profile"`
	Steps       []transcriptStep `json:"steps"`
	Termination string           `json:"termination,omitempty"`
}
type transcriptStep struct {
	PadBytes      int             `json:"pad_bytes,omitempty"`
	Send          json.RawMessage `json:"send,omitempty"`
	Raw           string          `json:"raw,omitempty"`
	Repeat        int             `json:"repeat,omitempty"`
	Expect        json.RawMessage `json:"expect,omitempty"`
	MessagePrefix string          `json:"message_prefix,omitempty"`
}

func TestProtocolTranscripts(t *testing.T) {
	paths, err := filepath.Glob("../docs/protocol/v2/transcripts/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no protocol transcripts found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture transcript
			if err := json.Unmarshal(b, &fixture); err != nil {
				t.Fatal(err)
			}
			if fixture.Status != "" && fixture.Status != "observed" && fixture.Status != "proposed" {
				t.Fatalf("unknown status %q", fixture.Status)
			}
			if fixture.Termination != "" && fixture.Termination != "frame-too-large" {
				t.Fatalf("unknown termination %q", fixture.Termination)
			}
			if fixture.Status == "proposed" {
				t.Skip("proposed, not v1-normative: " + fixture.Finding)
			}
			var p Plugin
			switch fixture.Profile {
			case "base":
				p = &transcriptBase{}
			case "full":
				p = &transcriptFull{}
			case "lifecycle-error":
				p = &transcriptLifecycleError{}
			case "health-error":
				p = &transcriptHealthError{}
			default:
				t.Fatalf("unknown profile %q", fixture.Profile)
			}
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			t.Cleanup(func() { inW.Close(); inR.Close(); outW.Close(); outR.Close() })
			done := make(chan error, 1)
			go func() { err := serveWith(p, inR, outW); inR.Close(); outW.Close(); done <- err }()
			responses := make(chan json.RawMessage, len(fixture.Steps)+1)
			stopped := make(chan struct{})
			t.Cleanup(func() { close(stopped) })
			readDone := make(chan error, 1)
			go func() {
				reader := bufio.NewReader(outR)
				for {
					b, err := reader.ReadBytes('\n')
					if err != nil {
						readDone <- err
						close(responses)
						return
					}
					select {
					case responses <- json.RawMessage(b):
					case <-stopped:
						return
					}
				}
			}()
			for i, step := range fixture.Steps {
				var compact bytes.Buffer
				if len(step.Send) > 0 {
					if err := json.Compact(&compact, step.Send); err != nil {
						t.Fatal(err)
					}
				}
				frame := compact.String()
				if step.Raw != "" {
					frame = step.Raw
				}
				if step.PadBytes > 0 {
					if step.PadBytes < len(frame) {
						t.Fatal("pad_bytes smaller than frame")
					}
					frame += strings.Repeat(" ", step.PadBytes-len(frame))
				}
				if step.Repeat > 0 {
					frame = strings.Repeat(frame, step.Repeat)
				}
				// An over-limit frame can cause the server to close input mid-write.
				written := make(chan error, 1)
				go func() { _, err := io.WriteString(inW, frame+"\n"); written <- err }()
				select {
				case err := <-written:
					if err != nil && fixture.Termination == "" {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("step %d: stdin write timeout", i)
				}
				if len(step.Expect) == 0 {
					continue
				}
				select {
				case got, ok := <-responses:
					if !ok {
						t.Fatalf("step %d: stdout closed", i)
					}
					compareTranscriptResponse(t, i, got, step)
				case <-time.After(5 * time.Second):
					t.Fatalf("step %d: response timeout", i)
				}
			}
			inW.Close()
			// Drain to EOF as well: notifications must never produce a late response.
			for {
				select {
				case got, ok := <-responses:
					if ok {
						t.Errorf("unexpected response: %s", got)
						continue
					}
				case <-time.After(5 * time.Second):
					t.Fatal("stdout EOF timeout")
				}
				break
			}
			if err := <-readDone; !errors.Is(err, io.EOF) {
				t.Fatalf("stdout: %v", err)
			}
			select {
			case err := <-done:
				if fixture.Termination == "" && err != nil {
					t.Fatal(err)
				}
				if fixture.Termination != "" && (err == nil || !strings.Contains(err.Error(), "stdin scanner: bufio.Scanner: token too long")) {
					t.Fatalf("Serve error = %v, want %q", err, fixture.Termination)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve exit timeout")
			}
		})
	}
}

func compareTranscriptResponse(t *testing.T, step int, got json.RawMessage, want transcriptStep) {
	t.Helper()
	var actual, expected map[string]any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want.Expect, &expected); err != nil {
		t.Fatal(err)
	}
	if want.MessagePrefix != "" {
		rpcErr, ok := actual["error"].(map[string]any)
		if !ok {
			t.Fatalf("step %d: expected RPC error, got %s", step, got)
		}
		message, ok := rpcErr["message"].(string)
		if !ok || !strings.HasPrefix(message, want.MessagePrefix) {
			t.Fatalf("step %d: error message = %v", step, rpcErr["message"])
		}
		// Decoder wording varies between languages. Only this field is relaxed.
		delete(rpcErr, "message")
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("step %d:\n got %s\nwant %s", step, got, want.Expect)
	}
}

type transcriptBase struct{}

func (*transcriptBase) Init(_ context.Context, _ InitParams) (InitResult, error) {
	return InitResult{ID: "fixture", Name: "Fixture", Version: "1.0.0", Description: "conformance", Protocol: 2, CapabilityContract: 1}, nil
}
func (*transcriptBase) Load(context.Context) (LoadResult, error) {
	return LoadResult{SkippedRegistrations: []SkippedRegistration{{Kind: "command", ID: "optional", Reason: "no config"}}}, nil
}
func (*transcriptBase) Unload(context.Context) error { return nil }

type transcriptFull struct{ transcriptBase }

func fixtureError(name string) error {
	switch name {
	case "not-found":
		return fmt.Errorf("wrapped: %w", plugin.ErrNotFound("missing"))
	case "conflict":
		return plugin.ErrConflict("exists")
	case "validation":
		return plugin.ErrValidation("invalid")
	case "cancelled":
		return fmt.Errorf("wrapped: %w", plugin.ErrCancelled)
	case "internal":
		return errors.New("failed")
	case "other-status":
		return &plugin.Error{Code: 403, Message: "denied"}
	case "panic":
		panic("fixture panic")
	}
	return nil
}
func (*transcriptFull) Command(_ context.Context, r CommandRequest) (CommandResult, error) {
	if err := fixtureError(r.Name); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Action: "message", Content: r.Args, Envelopes: []plugin.EnvelopeOut{{Type: "fixture.echo", Data: map[string]any{"name": r.Name, "identity": r.Identity}, SessionID: r.SessionID}}}, nil
}
func (*transcriptFull) EventHandle(_ context.Context, r EventRequest) (EventResult, error) {
	if err := fixtureError(r.Type); err != nil {
		return EventResult{}, err
	}
	if r.PreHook {
		return EventResult{Cancel: true, Reason: "veto"}, nil
	}
	return EventResult{}, nil
}
func (*transcriptFull) Create(_ context.Context, _ string, d map[string]any) (map[string]any, error) {
	return d, nil
}
func (*transcriptFull) Read(_ context.Context, rt, id string) (map[string]any, error) {
	if err := fixtureError(id); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "resource_type": rt}, nil
}
func (*transcriptFull) Update(_ context.Context, _ string, _ string, d map[string]any) (map[string]any, error) {
	return d, nil
}
func (*transcriptFull) Delete(context.Context, string, string) error { return nil }
func (*transcriptFull) List(context.Context, string, map[string]any) ([]map[string]any, error) {
	return nil, nil
}
func (*transcriptFull) MCPCallTool(_ context.Context, r MCPCallRequest) (MCPCallResult, error) {
	if err := fixtureError(r.ToolName); err != nil {
		return MCPCallResult{}, err
	}
	b, _ := json.Marshal(r.Arguments)
	return MCPCallResult{Content: b, IsError: r.ToolName == "tool-error"}, nil
}
func (*transcriptFull) HTTPHandle(_ context.Context, r HTTPRequest) (HTTPResponse, error) {
	if err := fixtureError(r.Path); err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{Status: 201, Headers: map[string]string{"content-type": "application/octet-stream"}, Body: r.Body}, nil
}
func (*transcriptFull) Migrate(_ context.Context, from, to string) error { return fixtureError(from) }
func (*transcriptFull) Health(context.Context) (HealthStatus, error) {
	return HealthStatus{OK: true}, nil
}

type transcriptLifecycleError struct{ transcriptBase }

func (*transcriptLifecycleError) Init(context.Context, InitParams) (InitResult, error) {
	return InitResult{}, plugin.ErrNotFound("missing")
}
func (*transcriptLifecycleError) Load(context.Context) (LoadResult, error) {
	return LoadResult{}, plugin.ErrCancelled
}

type transcriptHealthError struct{ transcriptBase }

func (*transcriptHealthError) Health(context.Context) (HealthStatus, error) {
	return HealthStatus{}, errors.New("unhealthy")
}
