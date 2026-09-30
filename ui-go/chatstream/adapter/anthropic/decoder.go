package anthropic

import (
	"bytes"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/internal/anthropicwire"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

type decoder struct {
	*decodekit.Base
	m *anthropicwire.Machine
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	b := decodekit.New(o)
	return &decoder{Base: b, m: anthropicwire.NewMachine(b, anthropicwire.Config{OwnsRun: true, EmitUsage: true})}
}

func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	if d.Terminated() || len(bytes.TrimSpace(f.Data)) == 0 {
		return nil, nil
	}
	typ := f.Event
	out, handled := d.m.Handle(nil, typ, f.Data)
	if !handled {
		out = d.Emit(out, d.m.RawUnknown(f.Data, typ))
	}
	return out, nil
}

// Close ends the stream: nothing if the run already ended, else the open parts
// are closed and a run.error with code upstream_truncated ends it.
func (d *decoder) Close(cause error) []chatstream.Event { return d.Base.Close(cause) }
