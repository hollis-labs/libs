//go:build linux || darwin

package claudeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigFifoSwapNeverBlocks(t *testing.T) {
	if os.Getenv("TRUST_FIFO_WITNESS") == "1" {
		r := request(t)
		path := filepath.Join(r.Config.Path, configName)
		if e := os.WriteFile(path, []byte(`{}`), 0600); e != nil {
			t.Fatal(e)
		}
		p := New()
		p.openConfig = func(root *os.Root, rel string) (*os.File, error) {
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			if e := syscall.Mkfifo(path, 0600); e != nil {
				t.Fatal(e)
			}
			return openConfigReadOnly(root, rel)
		}
		if _, e := p.Observe(context.Background(), r); e == nil {
			t.Fatal("swapped FIFO accepted")
		}
		return
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestConfigFifoSwapNeverBlocks$")
	cmd.Env = append(os.Environ(), "TRUST_FIFO_WITNESS=1")
	output, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("config open blocked after FIFO swap")
	}
	if e != nil {
		t.Fatalf("worker: %v %s", e, output)
	}
}
func TestOpenedConfigurationMustRemainSafe(t *testing.T) {
	r := request(t)
	path := filepath.Join(r.Config.Path, configName)
	if e := os.WriteFile(path, []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	p := New()
	p.openConfig = func(root *os.Root, rel string) (*os.File, error) {
		f, e := openConfigReadOnly(root, rel)
		if e != nil {
			return nil, e
		}
		if e := os.Chmod(path, 0622); e != nil {
			t.Fatal(e)
		}
		return f, nil
	}
	if _, e := p.Observe(context.Background(), r); e == nil {
		t.Fatal("unsafe opened configuration accepted")
	}
}
func TestConfigurationTokenWalkHasBoundedDepth(t *testing.T) {
	raw := strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001)
	if uniqueValue(json.NewDecoder(strings.NewReader(raw))) == nil {
		t.Fatal("unbounded JSON token nesting accepted")
	}
}
func TestOutputSizeLimitRefusesBeforeReplacement(t *testing.T) {
	r := request(t)
	p := New()
	path := filepath.Join(r.Config.Path, configName)
	if e := os.WriteFile(path, []byte(`{"padding":""}`), 0600); e != nil {
		t.Fatal(e)
	}
	apply := func() (bool, error) {
		s, e := p.Begin(context.Background(), r)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		_, changed, e := s.Apply(context.Background(), r, func(context.Context) error { return nil })
		return changed, e
	}
	if _, e := apply(); e != nil {
		t.Fatal(e)
	}
	sample, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	padding := strings.Repeat("x", maxConfig-len(sample)+1)
	before, e := json.Marshal(map[string]string{"padding": padding})
	if e != nil {
		t.Fatal(e)
	}
	if len(before) > maxConfig {
		t.Fatal("input fixture exceeds read limit")
	}
	if e := os.WriteFile(path, before, 0600); e != nil {
		t.Fatal(e)
	}
	changed, e := apply()
	if e == nil || changed {
		t.Fatal("oversized result replaced configuration", changed, e)
	}
	after, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused oversized result changed configuration")
	}
}
func TestReadonlyConfigurationRootRefusedBeforeLock(t *testing.T) {
	r := request(t)
	if e := os.WriteFile(filepath.Join(r.Config.Path, configName), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(r.Config.Path, 0555); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(r.Config.Path, 0700)
	if _, e := New().Observe(context.Background(), r); e == nil {
		t.Fatal("unwritable config root accepted")
	}
	if _, e := os.Stat(filepath.Join(r.Config.Path, lockName)); !os.IsNotExist(e) {
		t.Fatal("preflight created lock", e)
	}
}
func TestUnpairedUnicodeEscapesRefusedWithoutNormalization(t *testing.T) {
	for _, raw := range []string{`{"\ud800":1}`, `{"\udc00":1}`, `{"field":"\ud800"}`, `{"field":"\udc00"}`, `{"field":"\ud800\u0041"}`} {
		t.Run(raw, func(t *testing.T) {
			r := request(t)
			path := filepath.Join(r.Config.Path, configName)
			if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := New().Observe(context.Background(), r); e == nil {
				t.Fatal("lossy unicode text accepted")
			}
			after, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if string(after) != raw {
				t.Fatal("refused text changed")
			}
		})
	}
}
func TestPairedUnicodeEscapesPreserved(t *testing.T) {
	r := request(t)
	path := filepath.Join(r.Config.Path, configName)
	if e := os.WriteFile(path, []byte(`{"\ud83d\ude00":{"value":"\ud83d\ude00"},"literal":"\\ud800"}`), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, changed, e := s.Apply(context.Background(), r, func(context.Context) error { return nil })
	if e != nil || !changed {
		t.Fatal(changed, e)
	}
	after, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var obj map[string]json.RawMessage
	if e := json.Unmarshal(after, &obj); e != nil {
		t.Fatal(e)
	}
	var value struct{ Value string }
	if json.Unmarshal(obj["😀"], &value) != nil || value.Value != "😀" {
		t.Fatal("paired value lost")
	}
	var literal string
	if json.Unmarshal(obj["literal"], &literal) != nil || literal != `\ud800` {
		t.Fatal("escaped literal changed")
	}
}
