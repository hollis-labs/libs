package credentials

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrInvalidHome = errors.New("credentials: captured home is invalid")

func absolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsRune(p, 0)
}
func under(base, p string) bool {
	r, err := filepath.Rel(base, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// ResolveRealHome validates explicit trusted host observations. It does not
// read the environment or resolve relative paths against an ambient cwd.
func ResolveRealHome(i CapturedProviderHome, o HomeObservations) (ResolvedHome, error) {
	if !i.BeforeRedirect || i.Provider == "" || i.Provenance == "" || i.CaptureID == "" || i.Revision == "" || !absolute(i.Path) || !absolute(i.AllowedBase) || !absolute(o.CanonicalHome) || !absolute(o.CanonicalBase) || !under(i.AllowedBase, i.Path) || !under(o.CanonicalBase, o.CanonicalHome) || len(i.PlantedRoots) == 0 || len(i.PlantedRoots) != len(o.CanonicalPlantedRoots) {
		return ResolvedHome{}, ErrInvalidHome
	}
	for n, p := range i.PlantedRoots {
		c := o.CanonicalPlantedRoots[n]
		if !absolute(p) || !absolute(c) || under(p, i.Path) || under(c, o.CanonicalHome) {
			return ResolvedHome{}, ErrInvalidHome
		}
	}
	return ResolvedHome{BeforeRedirect: true, LogicalPath: i.Path, CanonicalPath: o.CanonicalHome, CanonicalBase: o.CanonicalBase, Provider: i.Provider, CaptureID: i.CaptureID, Revision: i.Revision, Provenance: i.Provenance, PlantedRoots: append([]string(nil), o.CanonicalPlantedRoots...)}, nil
}
