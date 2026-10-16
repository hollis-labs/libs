package localfs

import "testing"

func TestKnownPlatformsRemainSupported(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		if !(&Port{platform: platform}).Supported() {
			t.Fatalf("platform %s lost support", platform)
		}
	}
	for _, platform := range []string{"windows", "plan9", ""} {
		if (&Port{platform: platform}).Supported() {
			t.Fatalf("platform %s unexpectedly supported", platform)
		}
	}
	var p *Port
	if p.Supported() {
		t.Fatal("nil port supported")
	}
}
