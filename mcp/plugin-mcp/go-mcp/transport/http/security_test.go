package httptransport

import (
	"crypto/tls"
	gmcp "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuardHostAndOrigin(t *testing.T) {
	tests := []struct {
		name, host, origin string
		opts               HandlerOptions
		tls                bool
		want               int
	}{
		{name: "loopback v4 CLI", host: "127.0.0.1:8080", want: 204},
		{name: "loopback v4 range", host: "127.1.2.3", want: 204},
		{name: "loopback v6 CLI", host: "[::1]:8080", want: 204},
		{name: "localhost CLI", host: "LOCALHOST:8080", want: 204},
		{name: "loopback browser", host: "localhost:8080", origin: "http://localhost:8080", want: 204},
		{name: "default port", host: "localhost:80", origin: "http://localhost", want: 204},
		{name: "TLS same origin", host: "localhost:443", origin: "https://localhost", tls: true, want: 204},
		{name: "other scheme", host: "localhost", origin: "https://localhost", want: 403},
		{name: "other port", host: "localhost:8080", origin: "http://localhost:8081", want: 403},
		{name: "rebinding no origin", host: "attacker.example:8080", want: 403},
		{name: "rebinding same origin", host: "attacker.example", origin: "http://attacker.example", want: 403},
		{name: "rebinding with trusted origin", host: "attacker.example", origin: "http://localhost", want: 403},
		{name: "localhost suffix", host: "localhost.attacker.example", want: 403},
		{name: "public IP", host: "192.0.2.1", want: 403},
		{name: "empty origin fragment", host: "localhost", origin: "http://localhost#", want: 403},
		{name: "bracketed hostname", host: "[localhost]", want: 403},
		{name: "null origin", host: "localhost", origin: "null", want: 403},
		{name: "empty hostname", host: "", want: 403},
		{name: "host userinfo", host: "attacker@localhost", want: 403},
		{name: "host slash", host: "localhost/path", want: 403},
		{name: "host trailing dot", host: "localhost.", want: 403},
		{name: "bad port", host: "localhost:99999", want: 403},
		{name: "empty port", host: "localhost:", want: 403},
		{name: "unbracketed IPv6", host: "::1", want: 403},
		{name: "IP zone", host: "[::1%25zone]", want: 403},
		{name: "origin userinfo", host: "localhost", origin: "http://attacker@localhost", want: 403},
		{name: "origin path", host: "localhost", origin: "http://localhost/path", want: 403},
		{name: "origin trailing slash", host: "localhost", origin: "http://localhost/", want: 403},
		{name: "origin query", host: "localhost", origin: "http://localhost?", want: 403},
		{name: "multiple origins", host: "localhost", origin: "http://localhost https://attacker.example", want: 403},
		{name: "hostname letters r and n", host: "server.example:8443", opts: HandlerOptions{AllowedHosts: []string{"server.example"}}, want: 204},
		{name: "host newline", host: "localhost\n", want: 403},
		{name: "host carriage return", host: "localhost\r", want: 403},
		{name: "host tab", host: "localhost\t", want: 403},
		{name: "configured public host", host: "mcp.example:8443", opts: HandlerOptions{AllowedHosts: []string{"mcp.example"}}, want: 204},
		{name: "configured public same origin", host: "mcp.example:8443", origin: "https://mcp.example:8443", tls: true, opts: HandlerOptions{AllowedHosts: []string{"mcp.example"}}, want: 204},
		{name: "configured port", host: "mcp.example:8443", opts: HandlerOptions{AllowedHosts: []string{"mcp.example:8443"}}, want: 204},
		{name: "configured port mismatch", host: "mcp.example:8444", opts: HandlerOptions{AllowedHosts: []string{"mcp.example:8443"}}, want: 403},
		{name: "explicit list replaces loopback", host: "localhost", opts: HandlerOptions{AllowedHosts: []string{"mcp.example"}}, want: 403},
		{name: "explicit IPv6", host: "[::1]:1234", opts: HandlerOptions{AllowedHosts: []string{"[::1]"}}, want: 204},
		{name: "explicit cross origin", host: "localhost", origin: "https://ui.example", opts: HandlerOptions{AllowedOrigins: []string{"https://ui.example"}}, want: 204},
		{name: "explicit origin replaces same origin", host: "localhost", origin: "http://localhost", opts: HandlerOptions{AllowedOrigins: []string{"https://ui.example"}}, want: 403},
		{name: "invalid host configuration fails closed", host: "localhost", opts: HandlerOptions{AllowedHosts: []string{"*", ""}}, want: 403},
		{name: "invalid origin configuration fails closed", host: "localhost", origin: "http://localhost", opts: HandlerOptions{AllowedOrigins: []string{"*", ""}}, want: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }), tt.opts)
			r := httptest.NewRequest("POST", "http://localhost/", nil)
			r.Host = tt.host
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			// Forwarded authorities must never make an attacker-controlled Host safe.
			r.Header.Set("X-Forwarded-Host", "localhost")
			r.Header.Set("Forwarded", "host=localhost;proto=https")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want || called != (tt.want == 204) {
				t.Fatalf("status=%d called=%v, want %d", w.Code, called, tt.want)
			}
		})
	}
}

func TestGuardRejectsDuplicateOriginHeaders(t *testing.T) {
	h := Guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler reached") }), HandlerOptions{})
	r := httptest.NewRequest("POST", "http://localhost/", nil)
	r.Header.Add("Origin", "http://localhost")
	r.Header.Add("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("status=%d", w.Code)
	}
}

// Exercise the real net/http local-address context: the SDK's built-in
// loopback heuristic must not override a deployment's explicit host policy.
func TestConfiguredHostThroughLoopbackListener(t *testing.T) {
	h := NewHandler(gmcp.NewServer("host-test", "1"), HandlerOptions{AllowedHosts: []string{"mcp.example:8443"}})
	ts := httptest.NewServer(h)
	defer ts.Close()
	for _, host := range []string{"mcp.example:8443", "attacker.example:8443"} {
		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		want := http.StatusMethodNotAllowed
		if host == "attacker.example:8443" {
			want = http.StatusForbidden
		}
		if resp.StatusCode != want {
			t.Fatalf("%s: status=%d want %d", host, resp.StatusCode, want)
		}
	}
}
