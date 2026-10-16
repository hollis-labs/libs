//go:build linux

package redact

import (
	"runtime"
	"slices"
	"syscall"
	"testing"
)

// BenchmarkSanitize4KBCPU measures aggregate process CPU, including GC workers,
// rather than elapsed scheduling delays. This is a mean, never a CPU p99 claim.
// Run alongside BenchmarkSanitize4KB for wall-clock p99 on a quiet host.
func BenchmarkSanitize4KBCPU(b *testing.B) {
	s := sanitizerFor(b, Policy{Mode: Denylist})
	payload := benchmarkPayload()
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	runtime.GC()
	var start, end syscall.Rusage
	b.ResetTimer()
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &start); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		_, r := s.Sanitize(payload)
		if r.ErrorClass != "" || !r.PII || r.Sensitivity != Secret {
			b.Fatal("invalid benchmark result")
		}
	}
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &end); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	cpu := usageNanos(end) - usageNanos(start)
	b.ReportMetric(float64(cpu)/float64(b.N), "cpu-ns/op")
}
func usageNanos(r syscall.Rusage) int64 {
	return (r.Utime.Sec+r.Stime.Sec)*1e9 + (r.Utime.Usec+r.Stime.Usec)*1e3
}

// BenchmarkSanitize4KBCPUSamples samples per-call process CPU, including GC
// workers. Getrusage has microsecond resolution and adds measurement overhead;
// both are retained rather than subtracted from the latency evidence.
func BenchmarkSanitize4KBCPUSamples(b *testing.B) {
	s := sanitizerFor(b, Policy{Mode: Denylist})
	payload := benchmarkPayload()
	samples := make([]int64, 0, min(b.N, 10000))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	runtime.GC()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var start, end syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &start); err != nil {
			b.Fatal(err)
		}
		_, report := s.Sanitize(payload)
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &end); err != nil {
			b.Fatal(err)
		}
		if report.ErrorClass != "" || !report.PII || report.Sensitivity != Secret {
			b.Fatal("invalid benchmark result")
		}
		if len(samples) < cap(samples) {
			samples = append(samples, usageNanos(end)-usageNanos(start))
		}
	}
	b.StopTimer()
	slices.Sort(samples)
	if len(samples) > 0 {
		b.ReportMetric(float64(samples[(len(samples)-1)/2]), "cpu-median-ns/op")
		b.ReportMetric(float64(samples[(len(samples)-1)*99/100]), "cpu-p99-ns/op")
	}
}
