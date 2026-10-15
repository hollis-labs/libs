package federation

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogAuditorLevelsAndShortFingerprint(t *testing.T) {
	var buf bytes.Buffer
	a := SlogAuditor(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	fp := strings.Repeat("ab", 32)

	a(AuditRecord{Peer: "beta", Fingerprint: fp, Op: OpSend, Allowed: true, Reason: "sent"})
	a(AuditRecord{Peer: "beta", Fingerprint: fp, Op: OpGet, Allowed: false, Reason: "not a party"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "level=INFO") || !strings.Contains(lines[0], "decision=allow") {
		t.Errorf("allowed record: %s", lines[0])
	}
	if !strings.Contains(lines[1], "level=WARN") || !strings.Contains(lines[1], "decision=deny") || !strings.Contains(lines[1], "not a party") {
		t.Errorf("refused record: %s", lines[1])
	}
	if strings.Contains(buf.String(), fp) || !strings.Contains(lines[0], "cert="+fp[:12]) {
		t.Errorf("the log must carry only a short fingerprint: %s", lines[0])
	}
	if got := shortFingerprint("abc"); got != "abc" {
		t.Errorf("shortFingerprint(short) = %q", got)
	}
}
