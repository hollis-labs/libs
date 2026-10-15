package render

import (
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"io/fs"
)

func normalizeMode(req Request, e artifact.Entry, field layout.Field, pkg bool, defaultMode fs.FileMode) (fs.FileMode, *Diagnostic, error) {
	if !e.Kind.Valid() || e.Kind == artifact.EntryFile && e.Mode.Type() != 0 || e.Kind == artifact.EntryDirectory && e.Mode.Type()&^fs.ModeDir != 0 {
		return 0, nil, &Diagnostic{Class: ClassRefusal, Code: CodeInvalidEntryType, Provider: req.Provider, Mode: req.Mode, Concern: field, Entry: e.Path, Reason: "managed entries must be regular files or directories"}
	}
	declared := e.Mode
	applied := declared.Perm() &^ 0022
	code := CodeClampedOverlayMode
	if pkg {
		code = CodeClampedPackageMode
	}
	if e.Kind == artifact.EntryDirectory {
		applied = 0755
		if pkg {
			code = CodeNormalizedPackageDirectoryMode
		} else {
			code = CodeNormalizedOverlayDirectoryMode
		}
	} else if applied == 0 {
		applied = defaultMode
	}
	changed := declared != 0 && declared != applied
	if e.Kind == artifact.EntryDirectory {
		// ModeDir describes the entry; special bits still require a diagnostic.
		const specialBits = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky | 07000
		changed = declared != 0 && (declared.Perm() != 0755 || declared&specialBits != 0)
	}
	if changed {
		return applied, &Diagnostic{Class: ClassInformational, Code: code, Provider: req.Provider, Mode: req.Mode, Concern: field, Reason: "entry permission mode was normalized", Entry: e.Path, Declared: declared, Applied: applied}, nil
	}
	return applied, nil, nil
}
