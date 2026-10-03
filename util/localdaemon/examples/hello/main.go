package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	localdaemon "github.com/hollis-labs/go-localdaemon"
)

func main() {
	dir, err := os.MkdirTemp("", "localdaemon-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The lock is the single-instance guard: hold it for the daemon's whole
	// life. The kernel drops it if the process dies, even on SIGKILL.
	lock, err := localdaemon.TryAcquire(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	// A second instance is refused.
	_, err = localdaemon.TryAcquire(lock.Path())
	var held *localdaemon.HeldError
	fmt.Println("second instance refused:", errors.As(err, &held))

	// The PID file is written atomically, so a reader never sees a torn file.
	pidFile := localdaemon.PIDFile{Path: filepath.Join(dir, "daemon.pid")}
	if err = pidFile.Write(os.Getpid()); err != nil {
		log.Fatal(err)
	}
	pid, err := pidFile.Read()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("pid recorded:", pid == os.Getpid(), "alive:", localdaemon.IsAlive(pid))
}
