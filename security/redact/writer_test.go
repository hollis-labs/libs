package redact_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/security/redact"
)

func stream(t *testing.T, r *redact.Redactor, in string, sizes func(i int) int, opts ...redact.WriterOption) string {
	t.Helper()
	var out bytes.Buffer
	w := r.Writer(&out, opts...)
	for i, pos := 0, 0; pos < len(in); i++ {
		n := min(max(sizes(i), 1), len(in)-pos)
		if _, err := w.Write([]byte(in[pos : pos+n])); err != nil {
			t.Fatal(err)
		}
		pos += n
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// However the stream is cut, the output equals redacting the whole text: byte at a
// time, every two-way split of each corpus case, and random chunking.
func TestWriterEqualsTextForAnyChunking(t *testing.T) {
	multi := "line one\nsecret-part-A\nsecret-part-B tail\nend\n"
	scope := redact.NewScope()
	scope.Add("multi", "secret-part-A\nsecret-part-B")
	r, err := redact.New(redact.WithScope(scope), redact.WithHeuristics())
	if err != nil {
		t.Fatal(err)
	}
	var inputs []string
	for _, c := range All() {
		inputs = append(inputs, c.Input, "before\n"+c.Input+"\nafter\n", c.Input+" trailing no newline")
	}
	inputs = append(inputs, multi, multi+j("-----BEGIN ", "PRIVATE KEY-----\n")+pemBody+j("\n-----END ", "PRIVATE KEY-----\nok\n"),
		j("-----BEGIN ", "PRIVATE KEY-----\n")+pemBody+"\n")
	rng := rand.New(rand.NewSource(7))
	for _, in := range inputs {
		want := r.Text(in)
		if got := stream(t, r, in, func(int) int { return 1 }); got != want {
			t.Fatalf("byte-at-a-time differs for %q:\n got  %q\n want %q", in, got, want)
		}
		step := 1
		if len(in) > 160 {
			step = 7 // every split of short inputs, a spread of long ones
		}
		for cut := 1; cut < len(in); cut += step {
			got := stream(t, r, in, func(i int) int {
				if i == 0 {
					return cut
				}
				return len(in)
			})
			if got != want {
				t.Fatalf("split at %d differs for %q:\n got  %q\n want %q", cut, in, got, want)
			}
		}
		for k := 0; k < 5; k++ {
			if got := stream(t, r, in, func(int) int { return 1 + rng.Intn(9) }); got != want {
				t.Fatalf("random chunking differs for %q:\n got  %q\n want %q", in, got, want)
			}
		}
	}
}

// A long unbroken line is flushed in bounded memory and a value inside the hold
// window that straddles the forced flush is still caught.
func TestWriterForcedFlushKeepsHold(t *testing.T) {
	r, _ := redact.New()
	secret := ghp
	in := strings.Repeat("x", 300) + " " + secret + " " + strings.Repeat("y", 300)
	for shift := 0; shift < 64; shift++ {
		pad := strings.Repeat("z", shift)
		var out bytes.Buffer
		w := r.Writer(&out, redact.WithMaxPending(128), redact.WithHold(64))
		for _, c := range []byte(pad + in) {
			if _, err := w.Write([]byte{c}); err != nil {
				t.Fatal(err)
			}
		}
		_ = w.Close()
		if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), redact.Marker) {
			t.Fatalf("shift %d: secret survived a forced flush", shift)
		}
	}
}

func TestWriterBoundedPending(t *testing.T) {
	r, _ := redact.New()
	var out countWriter
	w := r.Writer(&out, redact.WithMaxPending(1024), redact.WithHold(128))
	line := strings.Repeat("a", 100_000)
	for i := 0; i < 20; i++ {
		_, _ = w.Write([]byte(line))
	}
	if out.n < 1_900_000 { // everything except at most MaxPending+chunk may be held
		t.Fatalf("only %d of 2000000 bytes flushed before Close: pending is unbounded", out.n)
	}
	_ = w.Close()
	if out.n != 2_000_000 {
		t.Fatalf("total %d", out.n)
	}
}

type countWriter struct{ n int }

func (c *countWriter) Write(p []byte) (int, error) { c.n += len(p); return len(p), nil }

func TestWriterErrorsAndClose(t *testing.T) {
	r, _ := redact.New()
	boom := errors.New("boom")
	w := r.Writer(failWriter{boom})
	if _, err := w.Write([]byte("hello\n")); !errors.Is(err, boom) {
		t.Fatalf("destination error lost: %v", err)
	}
	var out bytes.Buffer
	w = r.Writer(&out)
	_, _ = w.Write([]byte("abc"))
	if out.Len() != 0 {
		t.Fatal("partial line emitted early")
	}
	if err := w.Close(); err != nil || out.String() != "abc" {
		t.Fatalf("Close: %v %q", err, out.String())
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("write after Close should fail")
	}
	if w.Close() != nil {
		t.Fatal("second Close should be a no-op")
	}
	if _, err := r.Writer(nil).Write([]byte("x")); err == nil {
		t.Fatal("nil destination should fail")
	}
	var short bytes.Buffer
	if _, err := io.Copy(r.Writer(&short), strings.NewReader("password=hunter22\n")); err != nil || strings.Contains(short.String(), "hunter22") {
		t.Fatalf("io.Copy: %v %q", err, short.String())
	}
}

type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }
