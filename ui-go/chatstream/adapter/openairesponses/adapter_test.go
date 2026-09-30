package openairesponses_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/go-chatstream/adapter/openairesponses"
	"github.com/hollis-labs/go-chatstream/conformance"
)

func TestFixtures(t *testing.T) {
	conformance.CheckDecoderDir(t, openairesponses.New(), "testdata")
}

func TestTruncationEveryPrefix(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(strings.TrimSuffix(filepath.Base(p), ".frames.json"), func(t *testing.T) {
			conformance.CheckTruncation(t, openairesponses.New(), fx)
		})
	}
}

func TestCapabilitiesAreValidAndHonest(t *testing.T) {
	a := openairesponses.New()
	c := a.Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Name() != "openai.responses" || a.Framing() != c.Framing {
		t.Errorf("name %q framing %v", a.Name(), a.Framing())
	}
	if !c.ReasoningRoundTrip || !c.ResumeCursor || c.Cancel {
		t.Errorf("capabilities = %+v", c)
	}
}
