package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"

	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

const PinCreationVersion = "workspace.pin.creation.v1"

// Spec returns detached frozen inputs for native consumers. Fresh authority and
// exact held-descriptor custody must still be earned after every callback.
func (p PlannedWorkspace) Spec() Spec { return copyRecord(p.spec) }

// PinCreationIntent is data, not a durable-intent token. The actual native path
// must successfully Record it through the SAME store under the full held EX
// union before creation; reading an intent-looking file cannot authorize replay.
func (p PlannedWorkspace) PinCreationIntent() (PinCreationEvidence, error) {
	if !p.valid || p.spec.Publication == nil {
		return PinCreationEvidence{}, refuse("pin_creation_request_required", "publication", Unsupported)
	}
	r := p.spec.Publication
	key := LockKey{Namespace: p.resources.LockNamespace, CanonicalID: p.spec.Boot.IdentityRoot.Path}
	if !slices.Contains(p.locks, key) {
		return PinCreationEvidence{}, refuse("pin_creation_union_mismatch", "publication", Conflict)
	}
	hash := sha256.Sum256([]byte(key.CanonicalID))
	e := PinCreationEvidence{Version: PinCreationVersion, Origin: publication.Origin{OperationID: p.spec.OperationID, InputDigest: p.digest, AgentURN: p.spec.Identity.AgentURN, IdentityKey: p.spec.Identity.EncodedKey}, Control: r.Control, Key: key, Path: filepath.Join(key.Namespace, "pin-"+hex.EncodeToString(hash[:])), JournalID: r.JournalID, ReservationID: r.ReservationID, Grant: r.PinCreationAuthorization}
	return e, e.Validate()
}

func (e PinCreationEvidence) Validate() error {
	if validateFrozenValues(e) != nil || e.Version != PinCreationVersion || e.Origin.OperationID == "" || e.JournalID == "" || e.ReservationID == "" || e.Control.Validate() != nil || e.Key.Namespace != e.Control.Path || !cleanAbsolute(e.Key.CanonicalID) || within(e.Control.Path, e.Key.CanonicalID) || within(e.Key.CanonicalID, e.Control.Path) {
		return refuse("pin_creation_binding", "publication", Conflict)
	}
	digest, err := hex.DecodeString(e.Origin.InputDigest)
	key, keyErr := bootkey.Encode(e.Origin.AgentURN)
	hash := sha256.Sum256([]byte(e.Key.CanonicalID))
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != e.Origin.InputDigest || keyErr != nil || key != e.Origin.IdentityKey || filepath.Base(e.Key.CanonicalID) != key || e.Path != filepath.Join(e.Key.Namespace, "pin-"+hex.EncodeToString(hash[:])) || e.Grant.Kind != PinCreationEffect || e.Grant.RootID == "" || e.Grant.AuthorizationID == "" || e.Grant.Version == "" {
		return refuse("pin_creation_binding", "publication", Conflict)
	}
	if e.Created && e.Identity.Inode == 0 || !e.Created && !e.Uncertain && e.Identity != (NativePinIdentity{}) {
		return refuse("pin_creation_observation", "publication", Conflict)
	}
	return nil
}

func admitPinCreationOrigins(r Receipt) ([]PinCreationOrigin, error) {
	out := copyRecord(r.PinOrigins)
	if r.PinCreation != nil {
		out = append(out, PinCreationOrigin{SchemaVersion: r.SchemaVersion, OperationID: r.OperationID, InputDigest: r.InputDigest, IdentityKey: r.IdentityKey, Evidence: *r.PinCreation})
	}
	operations := map[string]string{}
	for _, origin := range out {
		e := origin.Evidence
		if origin.SchemaVersion != SchemaVersion || origin.IdentityKey != r.IdentityKey || e.Validate() != nil || e.Origin.OperationID != origin.OperationID || e.Origin.InputDigest != origin.InputDigest || e.Origin.IdentityKey != origin.IdentityKey {
			return nil, refuse("pin_creation_origin_mismatch", "publication", Conflict)
		}
		if digest, exists := operations[origin.OperationID]; exists && digest != origin.InputDigest {
			return nil, refuse("pin_creation_origin_mismatch", "publication", Conflict)
		}
		operations[origin.OperationID] = origin.InputDigest
	}
	return out, nil
}
