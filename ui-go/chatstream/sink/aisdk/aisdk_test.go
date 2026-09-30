package aisdk_test

import (
	"testing"

	"github.com/hollis-labs/go-chatstream/sink"
	"github.com/hollis-labs/go-chatstream/sink/aisdk"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
)

func newEnc() sink.Encoder { return aisdk.New() }

func TestContract(t *testing.T) { sinktest.Contract(t, newEnc) }

func TestGolden(t *testing.T) {
	for _, sc := range sinktest.Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			sinktest.CheckGolden(t, ".", sc.Name, sinktest.Render(sinktest.Play(t, newEnc(), sc)))
		})
	}
}
