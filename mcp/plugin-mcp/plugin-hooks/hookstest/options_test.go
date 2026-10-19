package hookstest

import (
	"testing"
	"time"
)

func TestWaiverValidation(t *testing.T) {
	for _, option := range []Option{Waive("missing", "unsupported"), Waive("R01", " "), WithBound(0)} {
		config := &config{waivers: map[string]string{}, bound: time.Second}
		option(config)
		if validateConfig(config) == nil {
			t.Fatal("invalid waiver or observation bound accepted")
		}
	}
	config := &config{waivers: map[string]string{}, bound: time.Second}
	Waive("R01", "migration pending")(config)
	if err := validateConfig(config); err != nil {
		t.Fatal(err)
	}
}
