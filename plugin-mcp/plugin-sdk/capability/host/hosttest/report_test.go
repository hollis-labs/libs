package hosttest

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

// Embedding TB keeps the consumer reporting contract while recording failure
// calls instead of failing the enclosing self-test.
type recordingTB struct {
	testing.TB
	logs, failures []string
}

func (r *recordingTB) Logf(f string, args ...any) { r.logs = append(r.logs, fmt.Sprintf(f, args...)) }
func (r *recordingTB) Errorf(f string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(f, args...))
}

func requireViolation(t *testing.T, r Report, requirement, probe string) {
	t.Helper()
	for _, v := range r.Violations {
		if v.Requirement == requirement && v.Probe == probe {
			return
		}
	}
	t.Fatalf("missing behavioral failure %s/%s:\n%s", requirement, probe, r.String())
}
func requireStatus(t *testing.T, r Report, id, status string) {
	t.Helper()
	for _, q := range r.Requirements {
		if q.ID == id {
			if q.Status != status {
				t.Fatalf("%s status=%s want=%s", id, q.Status, status)
			}
			return
		}
	}
	t.Fatalf("requirement %s missing", id)
}
func TestReportFailuresReachConsumer(t *testing.T) {
	r := Evaluate(context.Background(), referenceAdapter{"claimed caller"}, Profile{})
	requireViolation(t, r, "C04", "delegated identity reaches audit")
	requireStatus(t, r, "C04", Failed)
	tb := &recordingTB{TB: t}
	RunProfile(tb, referenceAdapter{"claimed caller"}, Profile{})
	found := false
	for _, msg := range tb.failures {
		if strings.Contains(msg, "C04/delegated identity reaches audit") {
			found = true
		}
	}
	if !found {
		t.Fatal("RunProfile did not fail the caller's test for forged identity")
	}
	r = Evaluate(context.Background(), referenceAdapter{"publish reinterpreted"}, Profile{})
	for _, d := range r.Descriptors {
		if d.Name == capability.StorageWrite {
			if d.Status != Failed {
				t.Fatal("descriptor failure marked successful")
			}
			return
		}
	}
	t.Fatal("failed descriptor omitted")
}
func TestEmptyProfileFailsAndKeepsHostWideChecks(t *testing.T) {
	profile := Profile{Supported: []capability.Descriptor{}}
	for mode, expected := range map[string][2]string{
		"":                 {"C02", "profile"},
		"skip enforcement": {"C03", "direct absent binding"},
		"claimed caller":   {"C04", "delegated identity reaches audit"},
		"secret leak":      {"C08", "unsafe install does not widen grants and artifacts redact"},
	} {
		t.Run(mode, func(t *testing.T) {
			r := Evaluate(context.Background(), referenceAdapter{mode}, profile)
			requireViolation(t, r, "C02", "profile")
			requireStatus(t, r, "C02", Failed)
			requireViolation(t, r, expected[0], expected[1])
			if !strings.Contains(r.String(), capability.ReadonlyQuery+": "+DeclaredUnsupported) {
				t.Fatal("unsupported descriptor absent from report String")
			}
			tb := &recordingTB{TB: t}
			RunProfile(tb, referenceAdapter{mode}, profile)
			if len(tb.failures) == 0 {
				t.Fatal("empty profile did not fail consumer")
			}
		})
	}
}
func TestProfileVersionRejectedBeforeAdapter(t *testing.T) {
	f, _ := basic(capability.ReadonlyQuery)
	aboveUint32 := uint64(^uint32(0)) + 1
	for _, version := range []int{0, -1, int(aboveUint32)} {
		f.Catalog[0].SchemaVersion = version
		r := Evaluate(context.Background(), panicAdapter{}, Profile{Supported: f.Catalog})
		requireViolation(t, r, "C02", "profile")
		if r.Violations[0].Reason != "invalid descriptor version" {
			t.Fatal("invalid profile reached adapter")
		}
	}
}
func TestSuiteReasonsAndAdapterText(t *testing.T) {
	reason := "secret leaked to logs"
	if safeReason(suiteError(reason)) != reason {
		t.Fatal("suite reason withheld")
	}
	r := Evaluate(context.Background(), referenceAdapter{"secret only"}, Profile{})
	found := false
	for _, v := range r.Violations {
		if v.Requirement == "C08" && v.Reason == reason {
			found = true
		}
	}
	if !found {
		t.Fatal("secret leak reason not reported")
	}
}
func TestSecretScannerEncodingsAndSubstrings(t *testing.T) {
	secret := "K8zV3mP9aR2xQ7cN5uT4bL6yW1dF0sHj"
	for _, raw := range []string{secret, secret[4:], secret[8:16], secret[17:25], "Bearer " + secret} {
		for _, encoded := range []string{raw, base64.StdEncoding.EncodeToString([]byte(raw)), base64.RawStdEncoding.EncodeToString([]byte(raw)), base64.URLEncoding.EncodeToString([]byte(raw)), base64.RawURLEncoding.EncodeToString([]byte(raw)), hex.EncodeToString([]byte(raw)), strings.ToUpper(hex.EncodeToString([]byte(raw)))} {
			if !ContainsSecret("prefix "+encoded+" suffix", secret) {
				t.Fatalf("encoded substring missed: %q", encoded)
			}
		}
	}
	for _, value := range []string{"fixture-host", "fixture-trace", hex.EncodeToString([]byte("fixture-host")), hex.EncodeToString([]byte("fixture-trace"))} {
		if ContainsSecret(value, secret) {
			t.Fatal("unrelated identifier mistaken for secret")
		}
	}
	if ContainsSecret(secret[:7], secret) {
		t.Fatal("substring below floor matched")
	}
}

func TestCredentialLeakPathRequiresLongToken(t *testing.T) {
	r := Evaluate(context.Background(), shortAccessAdapter{}, Profile{})
	requireViolation(t, r, "C08", "unsafe install does not widen grants and artifacts redact")
	requireViolation(t, r, "C08", "secret diagnostic output")
}

type shortAccessAdapter struct{}

func (shortAccessAdapter) Open(ctx context.Context, f Fixture, o *Observer) (Instance, error) {
	i, e := referenceAdapter{}.Open(ctx, f, o)
	if e != nil {
		return i, e
	}
	if f.Secret != "" {
		return shortAccessInstance{i}, nil
	}
	return i, nil
}

type shortAccessInstance struct{ Instance }

func (i shortAccessInstance) Access() (string, string) {
	b, _ := i.Instance.Access()
	return b, "short-token"
}

func TestUnsupportedCallEnforcement(t *testing.T) {
	f, _ := basic(capability.ReadonlyQuery)
	r := Evaluate(context.Background(), referenceAdapter{"unsupported accepted"}, Profile{Supported: f.Catalog})
	for _, d := range capability.SharedDescriptors() {
		if d.Name != capability.ReadonlyQuery {
			requireViolation(t, r, "C10", d.Name+" declared unsupported refuses")
		}
	}
}

func extensionDescriptor(version int) capability.Descriptor {
	return capability.Descriptor{Name: "host.example.toy", SchemaVersion: version, Description: "Reviewed fixture read", EffectCeiling: capability.Read, Operations: []string{"toy/read"}, ScopeSchema: capability.ScopeSchema{Allowlists: []string{"operations", "targets", "effects"}}}
}
func TestExtensionOnlyProfile(t *testing.T) {
	for _, version := range []int{1, 7} {
		r := Evaluate(context.Background(), referenceAdapter{}, Profile{Supported: []capability.Descriptor{extensionDescriptor(version)}})
		if len(r.Violations) != 0 {
			t.Fatalf("extension-only version %d failed:\n%s", version, r.String())
		}
		requireStatus(t, r, "C02", Passed)
	}
}
func TestProfileNamesRejectedBeforeAdapter(t *testing.T) {
	d := extensionDescriptor(1)
	for _, name := range []string{"", "bad", "host.example.*", "host.example.Toy", "host.example.toy\n"} {
		bad := d
		bad.Name = name
		r := Evaluate(context.Background(), panicAdapter{}, Profile{Supported: []capability.Descriptor{bad}})
		requireViolation(t, r, "C02", "profile")
		if r.Violations[0].Reason != "invalid descriptor name or definition" {
			t.Fatalf("invalid name reached adapter: %s", r.String())
		}
	}
	for _, descriptors := range [][]capability.Descriptor{{d, d}, func() []capability.Descriptor {
		f, _ := basic(capability.ReadonlyQuery)
		return []capability.Descriptor{f.Catalog[0], f.Catalog[0]}
	}()} {
		r := Evaluate(context.Background(), panicAdapter{}, Profile{Supported: descriptors})
		requireViolation(t, r, "C02", "profile")
		if r.Violations[0].Reason != "duplicate descriptor name" {
			t.Fatal("duplicate profile reached adapter")
		}
	}
}
func TestSDKEnforcerUnsupportedRefusals(t *testing.T) {
	f, _ := basic(capability.ReadonlyQuery)
	// The ordinary reference dispatcher invokes the SDK Enforcer with no
	// catalog-before-grant shim. Its grant-first denials must be accepted.
	for _, p := range unsupportedProbes(f.Catalog) {
		if err := runProbe(context.Background(), referenceAdapter{}, p.run); err != nil {
			t.Fatalf("%s: %s", p.name, safeReason(err))
		}
	}
}

type deadlineTB struct {
	*recordingTB
	deadline time.Time
}

func (d deadlineTB) Deadline() (time.Time, bool) { return d.deadline, true }

type deadlineAdapter struct{ observed chan context.Context }

func (a deadlineAdapter) Open(ctx context.Context, _ Fixture, _ *Observer) (Instance, error) {
	a.observed <- ctx
	return nil, errors.New("deadline inspection adapter")
}
func TestRunProfileUsesTestDeadline(t *testing.T) {
	deadline := time.Now().Add(500 * time.Millisecond)
	observed := make(chan context.Context, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	defer func() { close(release); <-done }()
	tb := deadlineTB{&recordingTB{TB: t}, deadline}
	RunProfile(tb, nearDeadlineAdapter{observed, release, done}, Profile{Timeout: time.Hour})
	ctx := <-observed
	actual, ok := ctx.Deadline()
	if !ok || !actual.Before(deadline) || deadline.Sub(actual) > 100*time.Millisecond {
		t.Fatalf("watchdog deadline=%v; want margin before=%v", actual, deadline)
	}
	if !time.Now().Before(deadline) {
		t.Fatal("watchdog exhausted the test deadline")
	}
	if len(tb.failures) == 0 {
		t.Fatal("deadline did not fail consumer")
	}
	found := false
	for _, line := range tb.logs {
		if strings.Contains(line, "not_run") && strings.Contains(line, "remaining probes not run") {
			found = true
		}
	}
	if !found {
		t.Fatal("unfinished requirement reason missing from consumer logs")
	}
}

type nearDeadlineAdapter struct {
	observed chan context.Context
	release  chan struct{}
	done     chan struct{}
}

func (a nearDeadlineAdapter) Open(ctx context.Context, _ Fixture, _ *Observer) (Instance, error) {
	defer close(a.done)
	a.observed <- ctx
	<-a.release // Deliberately uncooperative, but joined by test cleanup.
	return nil, errors.New("fixture closed")
}

func TestEmbeddedBase64Alignments(t *testing.T) {
	secret := "K8zV3mP9aR2xQ7cN5uT4bL6yW1dF0sHj"
	for pad := 0; pad < 3; pad++ {
		for start := 0; start+8 <= len(secret); start++ {
			// Include only eight secret bytes, surrounded by unrelated bytes.
			line := strings.Repeat("p", pad) + secret[start:start+8] + " trailing-text"
			for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				if !ContainsSecret(enc.EncodeToString([]byte(line)), secret) {
					t.Fatalf("missed alignment=%d offset=%d", pad, start)
				}
			}
		}
	}
}
