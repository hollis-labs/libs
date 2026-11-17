package agentdir

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/sdk/transcript"
)

func TestStableProcessRestartWithTranscriptCursor(t *testing.T) {
	if os.Getenv("AGENTDIR_RESTART_HELPER") == "1" {
		root := os.Getenv("AGENTDIR_TEST_ROOT")
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		h := Host{Root: filepath.Join(root, "agents"), Store: TranscriptStableStore{Root: filepath.Join(root, "transcripts")}, Factory: &fakeFactory{}}
		fmt.Println(listener.Addr().String())
		if err := http.Serve(listener, h.Handler()); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	agents := filepath.Join(root, "agents")
	writeAgent(t, agents, "stable", "durable", "kind: stable\nlifetime: on-demand\nseat_id: seat-1\n")
	store := TranscriptStableStore{Root: filepath.Join(root, "transcripts")}
	snap, err := Load(filepath.Join(agents, "stable"))
	if err != nil {
		t.Fatal(err)
	}
	start := func() (*exec.Cmd, string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestStableProcessRestartWithTranscriptCursor$")
		cmd.Env = append(os.Environ(), "AGENTDIR_RESTART_HELPER=1", "AGENTDIR_TEST_ROOT="+root)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() { line, _ := bufio.NewReader(pipe).ReadString('\n'); ready <- strings.TrimSpace(line) }()
		select {
		case addr := <-ready:
			if addr == "" {
				t.Fatal("helper exited without listener")
			}
			return cmd, addr
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("helper did not start")
		}
		return nil, ""
	}
	stop := func(cmd *exec.Cmd) {
		t.Helper()
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
	}
	turn := func(addr, text string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/agents/stable/turns", strings.NewReader(fmt.Sprintf(`{"text":%q}`, text)))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "event: run.finish") {
			t.Fatalf("turn status %d: %s; %v", resp.StatusCode, body, err)
		}
	}
	first, addr := start()
	turn(addr, "before kill")
	entries, err := store.Replay(snap, transcript.Cursor{})
	if err != nil || len(entries) != 3 {
		t.Fatalf("initial transcript: %d entries, %v", len(entries), err)
	}
	cursor := entries[len(entries)-1].Cursor
	stop(first)
	second, addr := start()
	defer stop(second)
	turn(addr, "after restart")
	suffix, err := store.Replay(snap, cursor)
	if err != nil || len(suffix) != 2 || !strings.Contains(string(suffix[0].Record), "after restart") {
		t.Fatalf("replayed suffix: %d entries, %v", len(suffix), err)
	}
	all, err := store.Replay(snap, transcript.Cursor{})
	if err != nil || len(all) != 5 {
		t.Fatalf("persistent conversation: %d entries, %v", len(all), err)
	}
}
