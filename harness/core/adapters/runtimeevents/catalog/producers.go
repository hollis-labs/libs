package catalog

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed producers.json
var producersJSON []byte

// Producer is the set of kinds one app may publish through the hub ingest.
type Producer struct {
	// App is the envelope app value the producer's principal is bound to.
	App string `json:"app"`
	// Note records anything a reviewer should know, such as an unconfirmed
	// entry.
	Note string `json:"note,omitempty"`
	// Kinds are exact kinds, or prefixes when the entry ends in a dot.
	Kinds []string `json:"kinds"`
}

// Producers is the decoded producers.json.
type Producers struct {
	Version   int        `json:"version"`
	Note      string     `json:"note,omitempty"`
	Producers []Producer `json:"producers"`
}

var (
	producersOnce sync.Once
	producers     Producers
	producersApp  map[string]Producer
	producersErr  error
)

func loadProducers() {
	if producersErr = json.Unmarshal(producersJSON, &producers); producersErr != nil {
		return
	}
	producersApp = make(map[string]Producer, len(producers.Producers))
	for _, p := range producers.Producers {
		producersApp[p.App] = p
	}
}

// LoadProducers returns the per-app kind ownership map. It fails only if the
// embedded producers.json is malformed, which the package tests rule out.
func LoadProducers() (Producers, error) {
	producersOnce.Do(loadProducers)
	return producers, producersErr
}

// ProducerFor returns the producer entry registered for app.
func ProducerFor(app string) (Producer, bool) {
	producersOnce.Do(loadProducers)
	p, ok := producersApp[app]
	return p, ok
}

// Allows reports whether the producer may publish kind.
func (p Producer) Allows(kind string) bool {
	for _, k := range p.Kinds {
		if strings.HasSuffix(k, ".") {
			if strings.HasPrefix(kind, k) {
				return true
			}
		} else if kind == k {
			return true
		}
	}
	return false
}

// ProducerAllows reports whether app is a registered producer that may
// publish kind. An unregistered app is allowed nothing.
func ProducerAllows(app, kind string) bool {
	p, ok := ProducerFor(app)
	return ok && p.Allows(kind)
}
