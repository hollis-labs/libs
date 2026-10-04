// Package credentials provisions authorized logical resource links inside an
// inactive candidate. It never discovers a home or reads credential bytes.
// Completion is candidate-local and does not confer launch readiness.
package credentials

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

type CapturedProviderHome struct {
	Provider, Path, AllowedBase, Provenance, CaptureID, Revision string
	BeforeRedirect                                               bool
	PlantedRoots                                                 []string
}

type HomeObservations struct {
	CanonicalHome, CanonicalBase string
	CanonicalPlantedRoots        []string
}

type ResolvedHome struct {
	BeforeRedirect bool
	// Logical path survives atomic reauthentication; canonical path is policy evidence.
	LogicalPath, CanonicalPath, CanonicalBase, Provider, CaptureID, Revision, Provenance string
	PlantedRoots                                                                         []string
}

type Binding struct {
	Source, Destination                                         string
	Required                                                    bool
	AuthorizationID, AuthorizationVersion                       string
	SourceRead, SourceWrite                                     bool
	SourceWriteAuthorizationID, SourceWriteAuthorizationVersion string
}

type Group struct {
	Header    effects.Header
	Layer     string
	Candidate effects.RootInput
	Home      ResolvedHome
	Bindings  []Binding
}

type SourceObservation struct {
	LogicalPath, CanonicalPath string
	Accessible                 bool
}
type LinkObservation struct {
	Exists, IsLink                   bool
	Target, Identity, ParentIdentity string
}

type LinkPort interface {
	Supported() bool
	Source(context.Context, ResolvedHome, string) (SourceObservation, error)
	Destination(context.Context, effects.RootInput, string) (LinkObservation, error)
	OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error)
}

// CandidateSession confines operations to a verified private candidate and
// rechecks identity before mutation. No operation overwrites or creates parents.
type CandidateSession interface {
	Validate(context.Context) error
	Inspect(context.Context, string) (LinkObservation, error)
	CreateExclusive(context.Context, string, string) (LinkObservation, error)
	RemoveIfMatches(context.Context, string, LinkObservation) error
	Close() error
}

type PreparedGroup struct {
	group  Group
	digest string
}
