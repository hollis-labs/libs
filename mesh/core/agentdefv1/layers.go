package agentdef

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// skillsDir is the directory beside a layer's definitions that holds
// skills/<name>/SKILL.md trees. LoadLayers does not treat its contents as
// definitions.
const skillsDir = "skills"

// Layer is one source root a caller wants merged. It is just a directory
// tree: a "git checkout root" is a directory the host already checked out
// before calling LoadLayers. This package never does git operations.
type Layer struct {
	FS         fs.FS
	Root       string // directory within FS; "" means "."
	Name       string // for error messages and Definition.Layer
	Precedence int    // higher wins; equal/undeclared on a name collision => error
}

// Set is the merged result of LoadLayers: one Definition per name, plus every
// collision found (including resolved ones, for audit). A returned *Set is
// read-only and safe to share between goroutines.
type Set struct {
	ByName     map[string]*Definition
	Collisions []CollisionError
}

// CollisionError is a name claimed by two layers. When no precedence
// separates them it is an error returned from LoadLayers; when one layer
// outranks the other it is recorded in Set.Collisions with Resolved set.
type CollisionError struct {
	Name     string
	Layers   []string // layer Names, in the order encountered
	Resolved bool     // true: a declared precedence decided the winner
}

var _ error = (*CollisionError)(nil)

// Error describes the collision.
func (e *CollisionError) Error() string {
	if e.Resolved {
		return fmt.Sprintf("agentdef: name %q defined in layers %s (resolved by precedence)", e.Name, strings.Join(e.Layers, ", "))
	}
	return fmt.Sprintf("agentdef: name %q defined in layers %s with no declared precedence between them", e.Name, strings.Join(e.Layers, ", "))
}

// LoadLayers parses every *.md file under each layer's Root (skipping a
// top-level skills/ directory), keyed by parsed Name, applying Precedence to
// same-name collisions. A collision at equal precedence keeps the
// first-encountered definition, is appended to Set.Collisions, and is also
// returned (joined, discoverable with errors.As) as a non-nil error; the
// returned Set is still usable. A file that fails to parse aborts the load
// with a nil Set. LoadLayers holds no shared state and may be called
// concurrently.
func LoadLayers(layers []Layer) (*Set, error) {
	set := &Set{ByName: map[string]*Definition{}}
	prec := map[string]int{}
	var errs []error

	for _, l := range layers {
		if l.FS == nil {
			return nil, fmt.Errorf("agentdef: layer %q has no FS", l.Name)
		}
		root := l.Root
		if root == "" {
			root = "."
		}
		files, err := layerFiles(l.FS, root)
		if err != nil {
			return nil, fmt.Errorf("agentdef: layer %q: %w", l.Name, err)
		}
		for _, f := range files {
			d, err := ParseFile(l.FS, f)
			if err != nil {
				return nil, fmt.Errorf("agentdef: layer %q: %w", l.Name, err)
			}
			d.SourceRef = relTo(root, f)
			d.Layer = l.Name

			cur, exists := set.ByName[d.Name]
			if !exists {
				set.ByName[d.Name] = d
				prec[d.Name] = l.Precedence
				continue
			}
			c := CollisionError{Name: d.Name, Layers: []string{cur.Layer, l.Name}}
			switch curP := prec[d.Name]; {
			case l.Precedence > curP:
				c.Resolved = true
				set.ByName[d.Name] = d
				prec[d.Name] = l.Precedence
			case l.Precedence < curP:
				c.Resolved = true
			default:
				errs = append(errs, &c)
			}
			set.Collisions = append(set.Collisions, c)
		}
	}
	return set, errors.Join(errs...)
}

func layerFiles(fsys fs.FS, root string) ([]string, error) {
	var files []string
	err := fs.WalkDir(fsys, root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p == path.Join(root, skillsDir) {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(p) == ".md" {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

func relTo(root, p string) string {
	if root == "." {
		return p
	}
	return strings.TrimPrefix(p, root+"/")
}
