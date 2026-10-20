package catalog_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
)

var producerEntry = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*\.?$`)

func TestProducersAreWellFormed(t *testing.T) {
	ps, err := catalog.LoadProducers()
	if err != nil {
		t.Fatal(err)
	}
	if ps.Version != 1 || len(ps.Producers) == 0 {
		t.Fatalf("version %d, %d producers", ps.Version, len(ps.Producers))
	}
	seen := map[string]bool{}
	for _, p := range ps.Producers {
		if p.App == "" || seen[p.App] {
			t.Errorf("app %q is empty or duplicated", p.App)
		}
		seen[p.App] = true
		if len(p.Kinds) == 0 {
			t.Errorf("%s has no kinds", p.App)
		}
		for _, k := range p.Kinds {
			if !producerEntry.MatchString(k) {
				t.Errorf("%s: %q is not a kind or a dotted prefix", p.App, k)
			}
		}
	}
}

// Kind ownership is reviewed with the kind: every kind in the catalog that is
// not deprecated must be publishable by some registered app.
func TestEveryCatalogKindHasAProducer(t *testing.T) {
	c := mustLoad(t)
	ps, _ := catalog.LoadProducers()
	for _, e := range c.Kinds {
		if e.Status == catalog.StatusDeprecated {
			continue
		}
		owned := false
		for _, p := range ps.Producers {
			if p.Allows(e.Kind) {
				owned = true
				break
			}
		}
		if !owned {
			t.Errorf("kind %q is in the catalog but no app in producers.json may publish it", e.Kind)
		}
	}
}

func TestProducerAllows(t *testing.T) {
	cases := []struct {
		app, kind string
		want      bool
	}{
		{"cerberus", "approval.requested", true},
		{"cerberus", "task.state_changed", false},
		{"torque", "task.state_changed", true},
		{"torque", "fleet.node.degraded", true},
		{"torque", "fleet.node.down", false},
		{"torque", "daemon.started", true},
		{"torque", "daemon.shutdown_started", false},
		{"nobody", "task.state_changed", false},
		{"", "task.state_changed", false},
	}
	for _, c := range cases {
		if got := catalog.ProducerAllows(c.app, c.kind); got != c.want {
			t.Errorf("ProducerAllows(%q, %q) = %v, want %v", c.app, c.kind, got, c.want)
		}
	}
	if catalog.ProducerAllows("torque", strings.ToUpper("task.state_changed")) {
		t.Error("kind matching must be case sensitive")
	}
}
