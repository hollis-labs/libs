package subprocess

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"time"
	"unicode/utf8"
)

// DefaultFrameBytes includes the terminating LF and any accepted CR.
const DefaultFrameBytes = 8 * 1024 * 1024

// FrameLimits may narrow the symmetric implementation ceilings. Zero uses the
// default. Limits apply before Init and do not activate optional profiles.
type FrameLimits struct{ InputBytes, OutputBytes int }

// FrameTooLargeError carries bounded transport metadata, never frame contents.
type FrameTooLargeError struct {
	Direction string
	Limit     int
}

func (e *FrameTooLargeError) Error() string {
	return "subprocess: " + e.Direction + " frame exceeds byte limit " + strconv.Itoa(e.Limit)
}

var ErrTruncatedFrame = errors.New("subprocess: truncated input frame without LF")
var ErrFrameUTF8 = errors.New("subprocess: input frame is not valid UTF-8")

func frameLimits(limits FrameLimits) (FrameLimits, error) {
	if limits.InputBytes == 0 {
		limits.InputBytes = DefaultFrameBytes
	}
	if limits.OutputBytes == 0 {
		limits.OutputBytes = DefaultFrameBytes
	}
	if limits.InputBytes < 1 || limits.OutputBytes < 1 || limits.InputBytes > DefaultFrameBytes || limits.OutputBytes > DefaultFrameBytes {
		return limits, errors.New("subprocess: frame limits must be positive and may only narrow defaults")
	}
	return limits, nil
}

// readFrame stops at the first exceeded limit. It does not drain an attacker
// supplied oversized line or treat EOF as a delimiter. The buffered reader has
// fixed read-ahead; only bounded frame bytes are retained.
func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	line, _, err := readFrameCount(reader, limit)
	return line, err
}

func readFrameCount(reader *bufio.Reader, limit int) ([]byte, int, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > limit-len(frame) || (len(part) == limit-len(frame) && (len(part) == 0 || part[len(part)-1] != '\n')) {
			return nil, 0, &FrameTooLargeError{Direction: "input", Limit: limit}
		}
		frame = append(frame, part...)
		if err == nil {
			line := frame[:len(frame)-1]
			if bytes.HasSuffix(line, []byte{'\r'}) {
				line = line[:len(line)-1]
			}
			if !utf8.Valid(line) {
				return nil, 0, ErrFrameUTF8
			}
			return line, len(frame), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(frame) > 0 {
				return nil, 0, ErrTruncatedFrame
			}
			return nil, 0, io.EOF
		}
		return nil, 0, err
	}
}

// DefaultWriteTimeout bounds one whole transport write, independently of EOF.
const DefaultWriteTimeout = 5 * time.Second

var ErrWriteTimeout = errors.New("subprocess: stdout write deadline exceeded")
