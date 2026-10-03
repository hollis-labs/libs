package localdaemon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"time"

	localdaemon "github.com/hollis-labs/go-localdaemon"
)

func tempDir() string {
	dir, err := os.MkdirTemp("", "localdaemon-example-")
	if err != nil {
		panic(err)
	}
	return dir
}

func ExamplePIDFile() {
	dir := tempDir()
	defer func() { _ = os.RemoveAll(dir) }()

	f := localdaemon.PIDFile{Path: filepath.Join(dir, "daemon.pid")}
	_ = f.Write(os.Getpid()) // atomic: readers never see a partial file
	pid, _ := f.Read()
	fmt.Println(pid == os.Getpid())
	_ = f.Remove()
	_, err := f.Read()
	fmt.Println(errors.Is(err, os.ErrNotExist))
	// Output:
	// true
	// true
}

func ExampleIsAlive() {
	fmt.Println(localdaemon.IsAlive(os.Getpid()), localdaemon.IsAlive(0))
	// Output: true false
}

func ExampleTryAcquire() {
	dir := tempDir()
	defer func() { _ = os.RemoveAll(dir) }()

	lock, err := localdaemon.TryAcquire(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		panic(err)
	}
	defer func() { _ = lock.Release() }()

	_, err = localdaemon.TryAcquire(lock.Path())
	var held *localdaemon.HeldError
	fmt.Println(errors.As(err, &held), held.HolderPID == os.Getpid())
	// Output: true true
}

func ExampleAcquire() {
	dir := tempDir()
	defer func() { _ = os.RemoveAll(dir) }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := localdaemon.Acquire(ctx, filepath.Join(dir, "daemon.lock"))
	if err != nil {
		panic(err)
	}
	defer func() { _ = lock.Release() }()
	fmt.Println("acquired")
	// Output: acquired
}

func ExampleLock_SetInfo() {
	dir := tempDir()
	defer func() { _ = os.RemoveAll(dir) }()

	lock, _ := localdaemon.TryAcquire(filepath.Join(dir, "daemon.lock"))
	defer func() { _ = lock.Release() }()
	_ = lock.SetInfo([]byte(`{"listen":"tcp:127.0.0.1:7180"}`))

	_, err := localdaemon.TryAcquire(lock.Path())
	var held *localdaemon.HeldError
	if errors.As(err, &held) {
		fmt.Println(string(held.Info))
	}
	// Output: {"listen":"tcp:127.0.0.1:7180"}
}

func ExampleVerifyCommand() {
	// Confirm a PID still names your daemon before signaling it. Here the
	// process is this example's own, so any command line matches.
	ok, err := localdaemon.VerifyCommand(context.Background(), os.Getpid(), regexp.MustCompile(`.`))
	fmt.Println(ok, err)
	// Output: true <nil>
}

// Spawn re-executes the current binary, so it is shown but not run here.
func ExampleSpawn() {
	pid, err := localdaemon.Spawn(context.Background(), localdaemon.SpawnOptions{
		Args:   []string{"daemon", "run"},
		Detach: localdaemon.Session,
	})
	if err != nil {
		fmt.Println("spawn failed:", err)
		return
	}
	fmt.Println("started", pid)
}

func ExampleStop() {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	go func() { _ = cmd.Wait() }() // reap, so exit is visible to Stop

	opts := localdaemon.DefaultStopOptions()
	opts.Verify = func(ctx context.Context, pid int) (bool, error) {
		return localdaemon.VerifyCommand(ctx, pid, regexp.MustCompile(`sleep 60`))
	}
	fmt.Println(localdaemon.Stop(context.Background(), cmd.Process.Pid, opts))
	// Output: <nil>
}

func ExampleWaitReady() {
	var polls atomic.Int32
	err := localdaemon.WaitReady(context.Background(), 5*time.Second, 10*time.Millisecond,
		func(context.Context) (bool, error) { return polls.Add(1) >= 3, nil })
	fmt.Println(err)

	err = localdaemon.WaitReady(context.Background(), 30*time.Millisecond, 10*time.Millisecond,
		func(context.Context) (bool, error) { return false, nil })
	fmt.Println(errors.Is(err, localdaemon.ErrNotReady))
	// Output:
	// <nil>
	// true
}

func ExampleListener() {
	ln, err := localdaemon.Listener("tcp:127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = ln.Close() }()
	fmt.Println(ln.Addr().Network())
	// Output: tcp
}
