package federation

import (
	"fmt"
	"sort"
)

// Peer is a registered federation peer: a label and the authorities this install
// permits it to act for. A peer with no authority may do nothing.
type Peer struct {
	// Label is the operator-facing name, used in the audit log.
	Label string

	authorities map[string]struct{}
}

// IsAuthoritative reports whether the peer is registered for authority. This is
// the predicate behind the trust boundary: a peer may only originate mail for
// authorities it is registered for.
func (p *Peer) IsAuthoritative(authority string) bool {
	if p == nil || authority == "" {
		return false
	}
	_, ok := p.authorities[authority]
	return ok
}

// Authorities returns the peer's authorities, sorted.
func (p *Peer) Authorities() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.authorities))
	for a := range p.authorities {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// PeerConfig is one inbound peer as configured: a label, the SHA-256 fingerprints
// of the certificates it may present (a list, so a certificate can be rotated
// without downtime: every fingerprint resolves to the same peer), and the
// authorities it is registered for.
type PeerConfig struct {
	Label        string   `json:"label"`
	Fingerprints []string `json:"fingerprints"`
	Authorities  []string `json:"authorities"`
}

// PeerRegistry maps a pinned certificate fingerprint to the Peer it identifies.
// It is built once and read-only afterwards, so it is safe for concurrent use.
type PeerRegistry struct {
	byFingerprint map[string]*Peer
	peers         []*Peer
}

// NewPeerRegistry builds a registry. It fails on a peer with no label, no
// fingerprint or no authority, a malformed fingerprint, an empty authority, a
// duplicate label, and a fingerprint claimed by two peers: an ambiguous pin is a
// configuration error, not something to resolve arbitrarily.
func NewPeerRegistry(peers []PeerConfig) (*PeerRegistry, error) {
	reg := &PeerRegistry{byFingerprint: make(map[string]*Peer)}
	labels := map[string]struct{}{}
	for i, pc := range peers {
		if pc.Label == "" {
			return nil, fmt.Errorf("peer #%d: label is required", i)
		}
		if _, dup := labels[pc.Label]; dup {
			return nil, fmt.Errorf("peer label %q is used twice", pc.Label)
		}
		labels[pc.Label] = struct{}{}
		if len(pc.Fingerprints) == 0 {
			return nil, fmt.Errorf("peer %q: at least one pinned fingerprint is required", pc.Label)
		}
		if len(pc.Authorities) == 0 {
			return nil, fmt.Errorf("peer %q: at least one authority is required", pc.Label)
		}
		authorities := make(map[string]struct{}, len(pc.Authorities))
		for _, a := range pc.Authorities {
			if a == "" {
				return nil, fmt.Errorf("peer %q: authority entries must be non-empty", pc.Label)
			}
			authorities[a] = struct{}{}
		}
		peer := &Peer{Label: pc.Label, authorities: authorities}
		for _, fp := range pc.Fingerprints {
			norm, err := ValidateFingerprint(fp)
			if err != nil {
				return nil, fmt.Errorf("peer %q: %w", pc.Label, err)
			}
			if existing, dup := reg.byFingerprint[norm]; dup {
				return nil, fmt.Errorf("peer %q: fingerprint %s is already pinned to peer %q", pc.Label, norm, existing.Label)
			}
			reg.byFingerprint[norm] = peer
		}
		reg.peers = append(reg.peers, peer)
	}
	return reg, nil
}

// Lookup resolves a certificate fingerprint (in any form NormalizeFingerprint
// accepts) to its Peer.
func (r *PeerRegistry) Lookup(fingerprint string) (*Peer, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.byFingerprint[NormalizeFingerprint(fingerprint)]
	return p, ok
}

// PeerCount is the number of distinct peers.
func (r *PeerRegistry) PeerCount() int {
	if r == nil {
		return 0
	}
	return len(r.peers)
}

// PinCount is the number of pinned fingerprints across all peers (a peer in the
// middle of a rotation contributes two).
func (r *PeerRegistry) PinCount() int {
	if r == nil {
		return 0
	}
	return len(r.byFingerprint)
}
