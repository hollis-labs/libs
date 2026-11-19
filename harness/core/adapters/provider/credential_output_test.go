package provider

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const credentialOutputSentinel = "synthetic-auth-must-not-appear-in-failure-output"

// Exercise the failure boundary in a separate test process: neither a renderer
// regression nor this regression test's own diagnostics may echo credentials.
func TestCredentialFailureOutputRedacted(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "inherited-codex")
	for _, dir := range []string{codexHome, filepath.Join(home, ".codex")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal("could not create synthetic auth directory")
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), syntheticAuthJSON(t, credentialOutputSentinel), 0o600); err != nil {
			t.Fatal("could not seed synthetic auth")
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("could not locate test executable")
	}
	for _, mode := range []string{"isolation", "pure-failure", "legacy-failure"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(binary, "-test.run=^TestCredentialOutputSubprocess$", "-test.v")
			// Replace rather than filter the environment: no provider keys,
			// external auth roots, shell hooks or live-test opt-ins are inherited.
			cmd.Env = []string{
				"HOME=" + home, "USERPROFILE=" + home, "CODEX_HOME=" + codexHome,
				"TMPDIR=" + home, "TMP=" + home, "TEMP=" + home,
				"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
				"XDG_DATA_HOME=" + filepath.Join(home, "data"),
				"HARNESS_AUTH_OUTPUT_HELPER=" + mode,
			}
			if runtime.GOOS == "windows" {
				cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"))
			}
			out, err := cmd.CombinedOutput()
			// Never print captured output, including when this assertion fails.
			if strings.Contains(string(out), credentialOutputSentinel) {
				t.Fatal("subprocess disclosed synthetic credential material; output suppressed")
			}
			if mode == "isolation" {
				if err != nil {
					t.Fatal("legacy auth test did not isolate inherited synthetic auth; output suppressed")
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatal("expected a forced assertion failure; output suppressed")
			}
			if !strings.Contains(string(out), "credential content mismatch (values redacted)") {
				t.Fatal("forced failure did not exercise redacted credential assertion; output suppressed")
			}
		})
	}
}

func TestCredentialOutputSubprocess(t *testing.T) {
	mode := os.Getenv("HARNESS_AUTH_OUTPUT_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	if mode == "isolation" {
		TestBootDirSpec_CodexAuthLegacyOptIn(t)
		return
	}
	// Own the source even if someone accidentally sets the helper switch in a
	// normal test run: this helper must not make ambient auth readable either.
	home := t.TempDir()
	setHomeForTest(t, home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal("could not create synthetic auth source")
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), syntheticAuthJSON(t, credentialOutputSentinel), 0o600); err != nil {
		t.Fatal("could not seed synthetic auth source")
	}
	content, err := NewCodexAdapter().BootDirSpec().PlantedFiles[3].Render(PlantContext{LegacyAllowHostEffects: true})
	if err != nil || content == "" {
		t.Fatal("could not read synthetic auth fixture")
	}
	want := ""
	if mode == "legacy-failure" {
		want = string(syntheticAuthJSON(t, "different-synthetic-fixture"))
	}
	assertCredentialContent(t, content, want)
}

// Encode synthetic credentials as data, rather than secret-shaped source literals.
// Both subprocess sources and the deliberately mismatched value use this shape.
func syntheticAuthJSON(t testing.TB, value string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]string{"token": value})
	if err != nil {
		t.Fatal("could not encode synthetic auth fixture")
	}
	return data
}
