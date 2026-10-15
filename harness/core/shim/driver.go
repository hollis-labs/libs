package shim

import (
	"io"
	"os"
	"syscall"
	"time"
)

// transportDriver is the mechanical effect seam. This cut supports exact
// input bytes and process signals; future native codecs add supported effects
// here while the host continues to journal intent before invoking them.
type transportDriver interface {
	WriteInput([]byte) (int, error)
	SendSignal(syscall.Signal) error
}
type stdioDriver struct {
	input io.WriteCloser
	pid   int
}

func (d *stdioDriver) WriteInput(data []byte) (int, error) {
	if file, ok := d.input.(*os.File); ok {
		if err := file.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return 0, err
		}
	}
	return d.input.Write(data)
}
func (d *stdioDriver) SendSignal(s syscall.Signal) error { return signalGroup(d.pid, s) }
