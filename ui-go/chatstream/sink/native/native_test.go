package native_test

import (
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/native"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/sinktest"
)

func newEnc() sink.Encoder {
	return native.New(native.WithClock(func() time.Time { return sinktest.T0 }))
}

func TestContract(t *testing.T) { sinktest.Contract(t, newEnc) }

func TestGolden(t *testing.T) {
	for _, sc := range sinktest.Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			sinktest.CheckGolden(t, ".", sc.Name, sinktest.Render(sinktest.Play(t, newEnc(), sc)))
		})
	}
}
