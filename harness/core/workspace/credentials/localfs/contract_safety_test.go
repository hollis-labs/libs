//go:build linux || darwin

package localfs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestContractFifoSourceInsideHomeIsRefusedWithoutBlocking(t *testing.T) {
	g, _, p, _ := realFixture(t)
	fifo := filepath.Join(g.Home.LogicalPath, "c")
	os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skip("mkfifo unavailable")
	}
	done := make(chan error, 1)
	go func() {
		_, e := p.Source(context.Background(), g.Home, "c")
		done <- e
	}()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("fifo accepted as a source")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Source blocked opening a fifo")
	}
}

func TestContractCandidateModeLossDetectedByLiveSession(t *testing.T) {
	g, _, p, _ := realFixture(t)
	s, e := p.OpenCandidate(context.Background(), g.Candidate)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if err := os.Chmod(g.Candidate.Path, 0755); err != nil {
		t.Fatal(err)
	}
	if _, e := s.CreateExclusive(context.Background(), filepath.Join(g.Home.LogicalPath, "a"), "a"); e == nil {
		t.Fatal("session kept mutating after the candidate lost private mode")
	}
	if _, err := os.Lstat(filepath.Join(g.Candidate.Path, "a")); !os.IsNotExist(err) {
		t.Fatal("link created")
	}
}

func TestContractOpenCandidateRefusesIdentityAndBaseRoots(t *testing.T) {
	g, _, p, _ := realFixture(t)
	r := g.Candidate
	r.Path = r.MutationIdentity // candidate equal to the lock parent
	if s, e := p.OpenCandidate(context.Background(), r); e == nil {
		s.Close()
		t.Fatal("candidate equal to mutation identity accepted")
	}
	r = g.Candidate
	r.AllowedBase = r.Path // candidate equal to its allowed base
	if s, e := p.OpenCandidate(context.Background(), r); e == nil {
		s.Close()
		t.Fatal("candidate equal to allowed base accepted")
	}
}
