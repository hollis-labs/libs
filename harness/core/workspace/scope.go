package workspace

import "slices"

// Scope freezes explicit enrolled inputs without filesystem operations. It
// cannot establish enrollment, physical custody, isolation or readiness.
type Scope struct {
	spec      Spec
	resources Resources
	control   RootRef
	roots     []RootRef
	valid     bool
}

// ResolveScope preserves persisted identity keys and host-resolved paths. Fresh
// ephemeral enrollment is a host responsibility, never inferred from a label.
func ResolveScope(spec Spec, resources Resources, control RootRef) (Scope, error) {
	if err := validateFrozenValues(spec, resources, control); err != nil {
		return Scope{}, err
	}
	if spec.Operation == Install {
		return Scope{}, refuse("installed_scope_distinct", "scope", Conflict)
	}
	if err := spec.Validate(); err != nil {
		return Scope{}, err
	}
	if err := control.Validate(); err != nil {
		return Scope{}, err
	}
	if err := resources.LockRoot.Validate(); err != nil {
		return Scope{}, err
	}
	if resources.LockNamespace != resources.LockRoot.Path {
		return Scope{}, refuse("scope_lock_namespace_mismatch", "scope", Conflict)
	}
	roots, err := resolvedRoots(spec, resources)
	if err != nil {
		return Scope{}, err
	}
	for _, r := range roots {
		if r.ID == control.ID || r.ID == resources.LockRoot.ID || within(r.Path, control.Path) || within(control.Path, r.Path) || within(r.Path, resources.LockRoot.Path) || within(resources.LockRoot.Path, r.Path) {
			return Scope{}, refuse("scope_control_overlap", "scope", Conflict)
		}
	}
	if control != resources.LockRoot && (control.ID == resources.LockRoot.ID || within(control.Path, resources.LockRoot.Path) || within(resources.LockRoot.Path, control.Path)) {
		return Scope{}, refuse("scope_control_alias", "scope", Conflict)
	}
	return Scope{spec: copyRecord(spec), resources: copyRecord(resources), control: control, roots: roots, valid: true}, nil
}

func (s Scope) Valid() bool          { return s.valid }
func (s Scope) Spec() Spec           { return copyRecord(s.spec) }
func (s Scope) Resources() Resources { return copyRecord(s.resources) }
func (s Scope) Control() RootRef     { return s.control }
func (s Scope) Roots() []RootRef     { return slices.Clone(s.roots) }
