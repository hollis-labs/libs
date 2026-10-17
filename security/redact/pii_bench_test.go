package redact_test

import (
	"github.com/hollis-labs/libs/security/redact"
	"strings"
	"testing"
)

func BenchmarkText4KBDefault(b *testing.B) {
	benchmarkText4KB(b, redact.Default())
}

func benchmarkText4KB(b *testing.B, r *redact.Redactor) {
	seed := "worker completed a routine operation; next job queued. "
	text := strings.Repeat(seed, 4096/len(seed))
	text += strings.Repeat(" ", 4096-len(text))
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		r.Text(text)
	}
}

func BenchmarkText4KBPII(b *testing.B) {
	r, err := redact.New(redact.WithPII())
	if err != nil {
		b.Fatal(err)
	}
	benchmarkText4KB(b, r)
}

func BenchmarkText4KBMixed(b *testing.B) {
	seed := j("alice", "@", "example.test ", "GB82", "WEST", "123456", "98765432 ", "219-", "09-", "9999 ", "AB", "123456", "C ")
	input := seed + strings.Repeat("ordinary progress ", (4096-len(seed))/18)
	input += strings.Repeat(" ", 4096-len(input))
	for _, pii := range []bool{false, true} {
		name := "default"
		var opts []redact.Option
		if pii {
			name = "pii"
			opts = []redact.Option{redact.WithPII()}
		}
		b.Run(name, func(b *testing.B) {
			r, err := redact.New(opts...)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				r.Text(input)
			}
		})
	}
}
