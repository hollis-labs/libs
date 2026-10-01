//go:build unix

package tether

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func credentialHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("TETHER_TOKEN", "")
	return dir
}

func credentialFile(t *testing.T, path, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsLookupPrecedence(t *testing.T) {
	home := credentialHome(t)
	defaultFile := filepath.Join(home, ".tether", "run", "operator.token")
	explicitFile := filepath.Join(home, "explicit.token")
	credentialFile(t, defaultFile, "default-token")
	credentialFile(t, explicitFile, "file-token")
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, env, want string
		opts            []Option
	}{
		{"default", "", "Bearer default-token", nil},
		{"env", "env-token", "Bearer env-token", nil},
		{"explicit token", "env-token", "Bearer option-token", []Option{WithToken("option-token")}},
		{"explicit file", "env-token", "Bearer file-token", []Option{WithTokenFile(explicitFile)}},
		{"file wins", "env-token", "Bearer file-token", []Option{WithTokenFile(explicitFile), WithToken("option-token")}},
		{"anonymous", "env-token", "", []Option{WithToken("")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TETHER_TOKEN", tc.env)
			c, err := New(server.URL, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Health(context.Background()); err != nil {
				t.Fatal(err)
			}
			if seen != tc.want {
				t.Fatalf("wrong credential source: got=%q want=%q", seen, tc.want)
			}
		})
	}
}

func TestCredentialsFilesFailClosed(t *testing.T) {
	home := credentialHome(t)
	missing := filepath.Join(home, "missing.token")
	if _, err := New("http://127.0.0.1:1"); err != nil {
		t.Fatal("missing default file must support anonymous/offline bootstrap", err)
	}
	t.Setenv("TETHER_TOKEN", "env-token")
	if _, err := New("http://127.0.0.1:1", WithTokenFile(missing)); err == nil {
		t.Fatal("missing explicit file fell back")
	}
	path := filepath.Join(home, "good.token")
	credentialFile(t, path, "file-token")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New("http://127.0.0.1:1", WithTokenFile(path)); err == nil {
		t.Fatal("loose file accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "link.token")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(home, "fifo.token")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{link, fifo, home} {
		if _, err := New("http://127.0.0.1:1", WithTokenFile(bad)); err == nil {
			t.Fatal("special/symlink file accepted")
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 400), "secret\ninjected"} {
		credentialFile(t, path, bad)
		if _, err := New("http://127.0.0.1:1", WithTokenFile(path)); err == nil {
			t.Fatal("invalid file content accepted")
		}
	}
	for _, bad := range []string{"secret\nInjected: header", " secret", strings.Repeat("x", 257)} {
		_, err := New("http://127.0.0.1:1", WithToken(bad))
		if err == nil {
			t.Fatal("invalid bearer accepted")
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatal("error leaked secret")
		}
	}
	t.Setenv("TETHER_TOKEN", "")
	defaultFile := filepath.Join(home, ".tether", "run", "operator.token")
	credentialFile(t, defaultFile, "default-token")
	if err := os.Chmod(defaultFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New("http://127.0.0.1:1"); err == nil {
		t.Fatal("bad default file ignored")
	}
}

func TestCredentialsRedirectIsolationAndSharedClient(t *testing.T) {
	credentialHome(t)
	var reached bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token on redirect request")
		}
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer server.Close()
	shared := server.Client()
	original := shared.Transport
	c, err := New(server.URL, WithHTTPClient(shared), WithToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if shared.Transport != original {
		t.Fatal("caller-owned client mutated")
	}
	if _, err := c.Health(context.Background()); err == nil {
		t.Fatal("cross-origin redirect followed")
	}
	if reached {
		t.Fatal("foreign origin contacted")
	}
}

func TestCredentialsAcrossRequestPaths(t *testing.T) {
	credentialHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bearer on " + r.URL.Path)
		}
		switch r.URL.Path {
		case "/sessions/bootstrap":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"session_id":"s"}`)
		case "/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: daemon.started\ndata: {}\n\n")
		default:
			_, _ = io.WriteString(w, `{"items":[],"launches":[],"status":"ok"}`)
		}
	}))
	defer server.Close()
	c, err := New(server.URL, WithToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListLaunches(ctx); err != nil {
		t.Fatal(err)
	}
	// Use the two shared transport helpers directly to cover long-lived and
	// streaming clients without coupling this credential test to DTO schemas.
	if err := c.doNoBodyWithClient(ctx, c.longLivedClient(), http.MethodPost, "/sessions/s/turn", nil, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	resp, err := c.longLivedClient().Get(server.URL + "/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
