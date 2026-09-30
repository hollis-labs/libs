package openaichat_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/go-chatstream/adapter/openaichat"
	"github.com/hollis-labs/go-chatstream/conformance"
)

func TestFixtures(t *testing.T) {
	conformance.CheckDecoderDir(t, openaichat.New(), "testdata")
}

func TestTruncationEveryPrefix(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(strings.TrimSuffix(filepath.Base(p), ".frames.json"), func(t *testing.T) {
			conformance.CheckTruncation(t, openaichat.New(), fx)
		})
	}
}

func TestCapabilitiesAreValidAndHonest(t *testing.T) {
	a := openaichat.New()
	c := a.Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Name() != "openai.chat" || a.Framing() != c.Framing {
		t.Errorf("name %q framing %v", a.Name(), a.Framing())
	}
	if c.Reasoning != 0 || c.ReasoningRoundTrip || c.Citations || c.ResumeCursor || c.Approval != 0 {
		t.Errorf("declares something the dialect does not have: %+v", c)
	}
	if !c.ReasoningTokens || !c.CacheTokens {
		t.Errorf("usage breakdown is reported: %+v", c)
	}
}
