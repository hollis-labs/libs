package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

// ErrCoverageUnsupported means required content scope cannot be represented
// safely. An empty target list is never evidence of required coverage.
var ErrCoverageUnsupported = errors.New("snapshot: coverage unsupported")

// RootBinding identifies a host-resolved content root. These observations bind
// the plan; they are not a lease or permission to read a live filesystem.
type RootBinding struct {
	ID, Root, Provenance string
}

// TargetPolicy narrows effective grants. OptIn and OptOut are absolute paths;
// neither grants access. CredentialPaths names resolved host secret sources.
// Provider state, runtime, boot, control and scratch roots are excluded by
// default. A host may explicitly opt in nonsensitive scratch content, but may
// never override credential, deny or protect exclusions.
type TargetPolicy struct {
	Version         string
	Roots           []RootBinding
	OptIn, OptOut   []string
	CredentialPaths []string
}

// TargetPlan is immutable scope selected from concrete resolved grants. Its
// private exclusions must be honored before ingestion by a guarded provider.
// Targets alone are unsuitable for passing to the legacy raw ShadowGit.
type TargetPlan struct {
	roots  []plannedRoot
	digest string
}

type plannedRoot struct {
	Binding RootBinding
	Aliases []RootBinding
	Include []string
	Exclude []string
}

// Digest binds scope and provenance, not current authority or physical custody.
func (p TargetPlan) Digest() string { return p.digest }

// Bindings retains each logical root's provenance even when equal physical
// roots are captured once. It returns independent caller-owned data.
func (p TargetPlan) Bindings() []RootBinding {
	var out []RootBinding
	for _, r := range p.roots {
		out = append(out, r.Binding)
		out = append(out, r.Aliases...)
	}
	return out
}

// Targets returns a defensive copy of the public target map. Required secret
// exclusions are deliberately not encoded as caller-authored Git pathspecs.
func (p TargetPlan) Targets() []Target {
	out := make([]Target, 0, len(p.roots))
	for _, r := range p.roots {
		out = append(out, Target{ID: r.Binding.ID, Root: r.Binding.Root, IncludePaths: slices.Clone(r.Include)})
	}
	return out
}

// DeriveTargets selects scope without capturing, scheduling or granting access.
// Host policy must already have been intersected with authority. Default-allow
// legacy policies have no finite effective allowlist and are unsupported.
func DeriveTargets(access sandbox.ResolvedAccessPolicy, policy TargetPolicy) (TargetPlan, error) {
	if policy.Version == "" || access.ID == "" || access.Legacy.DefaultAllow || len(policy.Roots) == 0 {
		return TargetPlan{}, ErrCoverageUnsupported
	}
	allPaths := append(slices.Clone(policy.OptIn), policy.OptOut...)
	allPaths = append(allPaths, policy.CredentialPaths...)
	for _, p := range allPaths {
		if !cleanAbsolute(p) {
			return TargetPlan{}, ErrCoverageUnsupported
		}
	}
	bindings := slices.Clone(policy.Roots)
	slices.SortFunc(bindings, func(a, b RootBinding) int {
		if order := strings.Compare(a.Root, b.Root); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	seenID, seenRoot := map[string]bool{}, map[string]int{}
	plan := TargetPlan{}
	for _, b := range bindings {
		if b.ID == "" || b.Provenance == "" || seenID[b.ID] || !physicalDirectory(b.Root) {
			return TargetPlan{}, ErrCoverageUnsupported
		}
		seenID[b.ID] = true
		if index, ok := seenRoot[b.Root]; ok {
			plan.roots[index].Aliases = append(plan.roots[index].Aliases, b)
			continue
		}
		r := plannedRoot{Binding: b}
		for _, grant := range access.FS.Write {
			if !cleanAbsolute(grant.Path) {
				return TargetPlan{}, ErrCoverageUnsupported
			}
			selected := intersection(b.Root, grant.Path)
			if selected == "" {
				continue
			}
			if len(policy.OptIn) == 0 {
				if defaultExcluded(access, selected) {
					continue
				}
				r.Include = append(r.Include, relative(b.Root, selected))
			} else {
				for _, requested := range policy.OptIn {
					if p := intersection(selected, requested); p != "" {
						r.Include = append(r.Include, relative(b.Root, p))
					}
				}
			}
		}
		mandatory := append(slices.Clone(policy.CredentialPaths), policy.OptOut...)
		// Default exclusions also apply beneath a broad parent write grant.
		if len(policy.OptIn) == 0 {
			for _, root := range []string{access.Roots.Boot, access.Roots.State, access.Roots.Scratch} {
				if root != "" {
					mandatory = append(mandatory, root)
				}
			}
		}
		for _, runtime := range access.Runtime {
			mandatory = append(mandatory, runtime.Path)
		}
		for _, rules := range [][]sandbox.ResolvedPath{access.FS.Deny, access.FS.Protect, access.ProviderState.Read, access.ProviderState.Write, access.ProviderState.Deny, access.FS.SourceRead} {
			for _, rule := range rules {
				mandatory = append(mandatory, rule.Path)
			}
		}
		for _, p := range mandatory {
			if !cleanAbsolute(p) {
				return TargetPlan{}, ErrCoverageUnsupported
			}
			if i := intersection(b.Root, p); i != "" {
				r.Exclude = append(r.Exclude, relative(b.Root, i))
			}
		}
		// A nested declared root belongs exclusively to its own target.
		for _, child := range bindings {
			if child.Root != b.Root && within(b.Root, child.Root) {
				r.Exclude = append(r.Exclude, relative(b.Root, child.Root))
			}
		}
		r.Include = compactPaths(r.Include)
		r.Exclude = compactPaths(r.Exclude)
		r.Include = slices.DeleteFunc(r.Include, func(p string) bool {
			return slices.ContainsFunc(r.Exclude, func(e string) bool { return withinRelative(e, p) })
		})
		if len(r.Include) == 0 {
			return TargetPlan{}, ErrCoverageUnsupported
		}
		seenRoot[b.Root] = len(plan.roots)
		plan.roots = append(plan.roots, r)
	}
	// Every requested path must be covered; requests cannot widen grants.
	for _, p := range policy.OptIn {
		if !slices.ContainsFunc(access.FS.Write, func(g sandbox.ResolvedPath) bool { return within(g.Path, p) }) {
			return TargetPlan{}, ErrCoverageUnsupported
		}
		if !slices.ContainsFunc(plan.roots, func(r plannedRoot) bool { return r.allows(p) }) {
			return TargetPlan{}, ErrCoverageUnsupported
		}
	}
	encoded, _ := json.Marshal(struct {
		Version, Access string
		Roots           []plannedRoot
	}{policy.Version, access.ID, plan.roots})
	sum := sha256.Sum256(encoded)
	plan.digest = hex.EncodeToString(sum[:])
	return plan, nil
}

func (r plannedRoot) allows(abs string) bool {
	if !within(r.Binding.Root, abs) {
		return false
	}
	p := relative(r.Binding.Root, abs)
	return slices.ContainsFunc(r.Include, func(i string) bool { return withinRelative(i, p) }) &&
		!slices.ContainsFunc(r.Exclude, func(e string) bool { return withinRelative(e, p) })
}

func cleanAbsolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsRune(p, 0)
}
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
func withinRelative(root, p string) bool {
	return root == "." || p == root || strings.HasPrefix(p, root+"/")
}
func relative(root, p string) string { r, _ := filepath.Rel(root, p); return filepath.ToSlash(r) }
func intersection(a, b string) string {
	if within(a, b) {
		return b
	}
	if within(b, a) {
		return a
	}
	return ""
}
func physicalDirectory(p string) bool {
	if !cleanAbsolute(p) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || resolved != p {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
func compactPaths(paths []string) []string {
	slices.Sort(paths)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !slices.ContainsFunc(out, func(parent string) bool { return withinRelative(parent, p) }) {
			out = append(out, p)
		}
	}
	return out
}
func defaultExcluded(p sandbox.ResolvedAccessPolicy, selected string) bool {
	for _, root := range []string{p.Roots.Boot, p.Roots.State, p.Roots.Scratch} {
		if root != "" && within(root, selected) {
			return true
		}
	}
	for _, rule := range p.Runtime {
		if within(rule.Path, selected) {
			return true
		}
	}
	return false
}
