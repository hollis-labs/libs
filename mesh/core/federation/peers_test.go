package federation

import (
	"strings"
	"testing"
)

var fpA, fpB = strings.Repeat("a", 64), strings.Repeat("b", 64)

func TestPeerRegistryResolvesEveryPinnedFingerprintToTheSamePeer(t *testing.T) {
	reg, err := NewPeerRegistry([]PeerConfig{
		{Label: "one", Fingerprints: []string{fpA, strings.ToUpper(fpB)}, Authorities: []string{"z", "a", "a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p1, ok1 := reg.Lookup(fpA)
	p2, ok2 := reg.Lookup(fpB) // a rotation overlap: both pins are the same peer
	if !ok1 || !ok2 || p1 != p2 {
		t.Fatalf("lookups: %v %v %p %p", ok1, ok2, p1, p2)
	}
	if reg.PeerCount() != 1 || reg.PinCount() != 2 {
		t.Errorf("counts = %d peers, %d pins", reg.PeerCount(), reg.PinCount())
	}
	if got := p1.Authorities(); len(got) != 2 || got[0] != "a" || got[1] != "z" {
		t.Errorf("Authorities = %v, want sorted and de-duplicated", got)
	}
	if !p1.IsAuthoritative("a") || p1.IsAuthoritative("b") || p1.IsAuthoritative("") {
		t.Error("IsAuthoritative is wrong")
	}
	if _, ok := reg.Lookup(strings.Repeat("c", 64)); ok {
		t.Error("an unpinned fingerprint must not resolve")
	}
	var nilPeer *Peer
	if nilPeer.IsAuthoritative("a") || nilPeer.Authorities() != nil {
		t.Error("a nil Peer is authoritative for nothing")
	}
	var nilReg *PeerRegistry
	if _, ok := nilReg.Lookup(fpA); ok || nilReg.PeerCount() != 0 || nilReg.PinCount() != 0 {
		t.Error("a nil registry has no peers")
	}
}

func TestPeerRegistryRejectsAmbiguousOrIncompleteConfig(t *testing.T) {
	ok := PeerConfig{Label: "a", Fingerprints: []string{fpA}, Authorities: []string{"x"}}
	tests := map[string][]PeerConfig{
		"no label":        {{Fingerprints: []string{fpA}, Authorities: []string{"x"}}},
		"no fingerprints": {{Label: "a", Authorities: []string{"x"}}},
		"no authorities":  {{Label: "a", Fingerprints: []string{fpA}}},
		"empty authority": {{Label: "a", Fingerprints: []string{fpA}, Authorities: []string{""}}},
		"bad fingerprint": {{Label: "a", Fingerprints: []string{"zz"}, Authorities: []string{"x"}}},
		"shared pin":      {ok, {Label: "b", Fingerprints: []string{strings.ToUpper(fpA)}, Authorities: []string{"y"}}},
		"duplicate label": {ok, {Label: "a", Fingerprints: []string{fpB}, Authorities: []string{"y"}}},
		"same pin, same peer twice is fine only across one peer": {{Label: "a", Fingerprints: []string{fpA, fpA}, Authorities: []string{"x"}}},
	}
	for name, cfg := range tests {
		if _, err := NewPeerRegistry(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewPeerRegistry(nil); err != nil {
		t.Errorf("an empty registry is valid to construct: %v", err)
	}
}
