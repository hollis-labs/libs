package eventbus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustSpool(t *testing.T, dir string, max int64) *spool {
	t.Helper()
	s, err := openSpool(dir, max)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ids(items []queued) string {
	var out []string
	for _, q := range items {
		out = append(out, q.ID)
	}
	return strings.Join(out, ",")
}

func fileSize(t *testing.T, s *spool) int64 {
	t.Helper()
	st, err := os.Stat(s.filePath(s.gen))
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func TestPeekDoesNotConsumeUntilAck(t *testing.T) {
	dir := t.TempDir()
	s := mustSpool(t, dir, 1<<20)
	for _, id := range []string{"a", "b", "c"} {
		if _, _, err := s.append(item(id, PriorityHigh)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.peek(2)
	if ids(got) != "a,b" {
		t.Fatalf("peek = %s", ids(got))
	}
	// A new process sees all three: nothing was acknowledged.
	if again := mustSpool(t, dir, 1<<20); func() string { g, _ := again.peek(10); return ids(g) }() != "a,b,c" {
		t.Fatal("peek consumed records before ack")
	}
	if _, err := s.ackTo(got[1].end); err != nil {
		t.Fatal(err)
	}
	again := mustSpool(t, dir, 1<<20)
	g, _ := again.peek(10)
	if ids(g) != "c" {
		t.Fatalf("after ack = %s", ids(g))
	}
}

// Alternating peek/ack/append at a small cap must not let the file grow.
func TestFileSizeStaysBounded(t *testing.T) {
	const max = 200
	s := mustSpool(t, t.TempDir(), max)
	var worst int64
	for i := 0; i < 200; i++ {
		if _, _, err := s.append(item(fmt.Sprintf("e%03d", i), PriorityHigh)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.append(item(fmt.Sprintf("f%03d", i), PriorityHigh)); err != nil {
			t.Fatal(err)
		}
		got, err := s.peek(1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 1 {
			if _, err := s.ackTo(got[0].end); err != nil {
				t.Fatal(err)
			}
		}
		if sz := fileSize(t, s); sz > worst {
			worst = sz
		}
	}
	if limit := int64(max)*4/3 + 256; worst > limit {
		t.Fatalf("file reached %d bytes, limit %d", worst, limit)
	}
}

func TestTornTailIsRepairedBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	s := mustSpool(t, dir, 1<<20)
	if _, _, err := s.append(item("a", PriorityHigh)); err != nil {
		t.Fatal(err)
	}
	// A crash mid-write: half a record, no newline.
	f, err := os.OpenFile(s.filePath(s.gen), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"torn","p":1,"b":{"id":"to`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	again := mustSpool(t, dir, 1<<20)
	if _, _, err := again.append(item("b", PriorityHigh)); err != nil {
		t.Fatal(err)
	}
	got, _ := again.peek(10)
	if ids(got) != "a,b" {
		t.Fatalf("after repair = %s, want a,b", ids(got))
	}
}

func TestStrayAndStaleFilesAreCleanedUp(t *testing.T) {
	dir := t.TempDir()
	s := mustSpool(t, dir, 1<<20)
	if _, _, err := s.append(item("a", PriorityHigh)); err != nil {
		t.Fatal(err)
	}
	// Leftover of an interrupted swap, and a state head past the end.
	stray := filepath.Join(dir, "spool-99.jsonl")
	if err := os.WriteFile(stray, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spool.state"), []byte(fmt.Sprintf("%d 99999\n", s.gen)), 0o600); err != nil {
		t.Fatal(err)
	}
	again := mustSpool(t, dir, 1<<20)
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray generation file was not removed")
	}
	if again.head != again.size {
		t.Fatalf("head %d must be clamped to size %d", again.head, again.size)
	}
}

// Compaction and cap enforcement replace the file; the state moves with it, so
// a reopen after either sees the right unread records.
func TestReopenAfterCompactionAndCap(t *testing.T) {
	dir := t.TempDir()
	s := mustSpool(t, dir, 600)
	var all []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("h%d", i)
		all = append(all, id)
		if _, _, err := s.append(item(id, PriorityHigh)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.peek(2)
	if _, err := s.ackTo(got[1].end); err != nil { // forces a compaction
		t.Fatal(err)
	}
	again := mustSpool(t, dir, 600)
	g, _ := again.peek(10)
	if want := strings.Join(all[2:], ","); ids(g) != want {
		t.Fatalf("after compaction reopen = %s, want %s", ids(g), want)
	}
}

// Cap enforcement drops from the unloaded part only, so the offsets of records
// already loaded for sending stay valid.
func TestCapKeepsInFlightRecordsAndHighPriority(t *testing.T) {
	s := mustSpool(t, t.TempDir(), 700)
	if _, _, err := s.append(item("first", PriorityHigh)); err != nil {
		t.Fatal(err)
	}
	flight, _ := s.peek(1)
	for i := 0; i < 12; i++ {
		p := PriorityLow
		if i%4 == 0 {
			p = PriorityHigh
		}
		if _, _, err := s.append(item(fmt.Sprintf("n%02d", i), p)); err != nil {
			t.Fatal(err)
		}
	}
	if s.unread() > 700 {
		t.Fatalf("unread %d over cap", s.unread())
	}
	if _, err := s.ackTo(flight[0].end); err != nil {
		t.Fatalf("in-flight offset no longer valid: %v", err)
	}
	rest, _ := s.peek(50)
	got := ids(rest)
	for _, must := range []string{"n00", "n04", "n08"} {
		if !strings.Contains(got, must) {
			t.Errorf("high priority %s was dropped: %s", must, got)
		}
	}
	if strings.Contains(got, "n01") && !strings.Contains(got, "n11") {
		t.Errorf("oldest low priority should go first: %s", got)
	}
}

func TestUnreadableTailIsAcknowledged(t *testing.T) {
	dir := t.TempDir()
	s := mustSpool(t, dir, 1<<20)
	f, _ := os.OpenFile(s.filePath(s.gen), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("not a record\n")
	f.Close()
	again := mustSpool(t, dir, 1<<20)
	got, err := again.peek(10)
	if err != nil || len(got) != 0 {
		t.Fatalf("peek = %v, %v", got, err)
	}
	if again.unread() != 0 {
		t.Fatalf("unreadable lines left %d unread bytes", again.unread())
	}
}
