package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true, "127.1.2.3:80": true, "localhost:8080": true, "[::1]:8080": true, "tcp:127.0.0.1:8080": true,
		"unix:/tmp/a.sock": true, "/tmp/a.sock": true,
		"100.64.0.1:8080": false, "100.101.102.103:80": false, "[fd7a:115c:a1e0::1]:80": false, "host.tail1234.ts.net:80": false,
		"0.0.0.0:8080": false, ":8080": false, "10.0.0.5:80": false, "192.168.1.2:80": false, "example.com:80": false,
		"": false, "garbage": false, "127.0.0.1": false, // no port: does not parse
	} {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestModeWeakerThan(t *testing.T) {
	for _, tc := range []struct {
		m, than Mode
		want    bool
	}{
		{ModeOff, ModePermissive, true}, {ModeOff, ModeEnforce, true}, {ModePermissive, ModeEnforce, true},
		{ModeEnforce, ModePermissive, false}, {ModePermissive, ModeOff, false}, {ModeEnforce, ModeEnforce, false}, {ModeOff, ModeOff, false},
		{"OBSERVE", ModeEnforce, true}, {"ENFORCE", ModePermissive, false},
		{"bogus", ModeEnforce, false}, // unrecognized counts as enforce, so it is not weaker
		{ModeOff, "bogus", true},
	} {
		if got := tc.m.WeakerThan(tc.than); got != tc.want {
			t.Errorf("%q.WeakerThan(%q) = %v, want %v", tc.m, tc.than, got, tc.want)
		}
	}
}

func reloadGuard(t *testing.T, mode Mode, bind string, lg *slog.Logger) *Guard {
	t.Helper()
	g, err := New(Config{Mode: mode, Verifier: NewSwitch(nil), ReloadBind: bind, Logger: lg})
	if err != nil {
		t.Fatalf("New(%s, %q): %v (the startup mode is never judged by ReloadBind)", mode, bind, err)
	}
	return g
}

func TestReloadBindRefusesLooseningOffLoopback(t *testing.T) {
	for _, bind := range []string{"100.64.0.1:8080", "100.101.102.103:80", "host.tail1234.ts.net:80", "0.0.0.0:8080", ":8080", "10.0.0.5:80", "garbage"} {
		for _, tc := range []struct{ from, to Mode }{{ModeEnforce, ModePermissive}, {ModeEnforce, ModeOff}, {ModePermissive, ModeOff}} {
			g := reloadGuard(t, tc.from, bind, nil)
			for name, do := range map[string]func() error{
				"Apply":      func() error { return g.Apply(tc.to, nil) },
				"SetMode":    func() error { return g.SetMode(tc.to) },
				"CheckApply": func() error { return g.CheckApply(tc.to, nil) },
			} {
				err := do()
				if !errors.Is(err, ErrLoosenRefused) {
					t.Errorf("%s %s->%s on %q: want ErrLoosenRefused, got %v", name, tc.from, tc.to, bind, err)
				}
				if g.Mode() != tc.from {
					t.Errorf("%s %s->%s on %q: the mode changed to %s", name, tc.from, tc.to, bind, g.Mode())
				}
			}
		}
	}
}

func TestReloadBindAllowsSameModeAndTighteningAnywhere(t *testing.T) {
	for _, bind := range []string{"100.64.0.1:8080", "0.0.0.0:8080", "127.0.0.1:8080"} {
		for _, tc := range []struct{ from, to Mode }{
			{ModeEnforce, ModeEnforce}, {ModePermissive, ModePermissive}, {ModeOff, ModeOff},
			{ModeOff, ModePermissive}, {ModeOff, ModeEnforce}, {ModePermissive, ModeEnforce},
		} {
			g := reloadGuard(t, tc.from, bind, nil)
			if err := g.Apply(tc.to, NewSwitch(nil)); err != nil || g.Mode() != tc.to {
				t.Errorf("%s->%s on %q: err=%v mode=%s", tc.from, tc.to, bind, err, g.Mode())
			}
		}
	}
}

func TestReloadBindAllowsLooseningOnLoopbackAndLogsWarn(t *testing.T) {
	for _, bind := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "unix:/tmp/a.sock"} {
		logs := &lockedBuf{}
		g := reloadGuard(t, ModeEnforce, bind, slog.New(slog.NewTextHandler(logs, nil)))
		if err := g.Apply(ModePermissive, nil); err != nil || g.Mode() != ModePermissive {
			t.Fatalf("loopback %q loosening: err=%v mode=%s", bind, err, g.Mode())
		}
		out := logs.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "from=enforce") || !strings.Contains(out, "to=permissive") {
			t.Errorf("%q: the loosening must be logged at WARN naming old and new mode: %s", bind, out)
		}
	}
}

func TestReloadBindUnsetChangesNothing(t *testing.T) {
	g := reloadGuard(t, ModeEnforce, "", nil)
	if err := g.Apply(ModeOff, nil); err != nil {
		t.Fatalf("no ReloadBind: loosening must stay allowed (the caller decides): %v", err)
	}
}

func TestReloadBindDoesNotJudgeTheStartupMode(t *testing.T) {
	// A process may start token-gated at off/permissive on a non-loopback listener.
	for _, m := range []Mode{ModeOff, ModePermissive} {
		g := reloadGuard(t, m, "0.0.0.0:8080", nil)
		if err := g.Apply(m, NewSwitch(nil)); err != nil {
			t.Errorf("same-mode reload of a %s start on 0.0.0.0: %v", m, err)
		}
	}
}
