// Package localfs implements credential links using os.Root on Linux and macOS.
// It confines traversal for cooperating mutators under held mutation locks and
// private candidate custody. It does not protect against a same-user process
// renaming parents or swapping entries. Compensation rechecks parent and link
// identity before removal; uncertain custody retains links. There is no atomic
// compare-and-unlink claim and no credential content is read or copied.
package localfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hollis-labs/substrate/harness/sandbox/pathsafe"
	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

var (
	ErrUnsupported   = errors.New("credential links: platform unsupported")
	ErrSource        = credentials.ErrSourceUnavailable
	ErrSourceAbsent  = credentials.ErrSourceAbsent
	ErrSourceEscaped = credentials.ErrSourceEscaped
	ErrCustody       = errors.New("credential links: candidate custody unproven")
	ErrDestination   = errors.New("credential links: destination unavailable or conflicting")
)

type Port struct {
	platform   string
	openSource func(*os.Root, string) (*os.File, error)
}

func New() *Port                       { return &Port{platform: runtime.GOOS} }
func supportedOS(platform string) bool { return platform == "linux" || platform == "darwin" }
func (p *Port) Supported() bool        { return p != nil && supportedOS(p.platform) }

func contained(base, p string) bool {
	r, e := filepath.Rel(base, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func (p *Port) Source(ctx context.Context, h credentials.ResolvedHome, rel string) (credentials.SourceObservation, error) {
	if !p.Supported() {
		return credentials.SourceObservation{}, ErrUnsupported
	}
	if ctx.Err() != nil || credentials.ValidateRelPath(rel) != nil {
		return credentials.SourceObservation{}, ErrSource
	}
	real, e := filepath.EvalSymlinks(h.LogicalPath)
	if e != nil {
		if errors.Is(e, fs.ErrNotExist) {
			return credentials.SourceObservation{}, ErrSourceAbsent
		}
		return credentials.SourceObservation{}, ErrSource
	}
	if real != h.CanonicalPath {
		return credentials.SourceObservation{}, ErrSourceEscaped
	}
	resolved, e := pathsafe.ResolveUnder(h.LogicalPath, rel)
	if e != nil || !contained(h.CanonicalPath, resolved) {
		return credentials.SourceObservation{}, ErrSourceEscaped
	}
	for _, r := range h.PlantedRoots {
		if contained(r, resolved) {
			return credentials.SourceObservation{}, ErrSourceEscaped
		}
	}
	root, e := os.OpenRoot(h.LogicalPath)
	if e != nil {
		return credentials.SourceObservation{}, ErrSource
	}
	defer root.Close()
	// Opening read-only proves local accessibility without reading a byte. Only
	// regular files and directories are allowed, never devices or pipes.
	st, e := root.Stat(rel)
	if errors.Is(e, fs.ErrNotExist) {
		return credentials.SourceObservation{}, ErrSourceAbsent
	}
	if e != nil || (!st.Mode().IsRegular() && !st.IsDir()) || st.Mode().Perm()&0444 == 0 {
		return credentials.SourceObservation{}, ErrSource
	}
	probe := p.openSource
	if probe == nil {
		probe = openSourceReadOnly
	}
	f, e := probe(root, rel)
	if e != nil {
		return credentials.SourceObservation{}, ErrSource
	}
	opened, e := f.Stat()
	closeErr := f.Close()
	if e != nil || closeErr != nil || !os.SameFile(st, opened) || (!opened.Mode().IsRegular() && !opened.IsDir()) || opened.Mode().Perm()&0444 == 0 {
		return credentials.SourceObservation{}, ErrSource
	}
	return credentials.SourceObservation{LogicalPath: filepath.Join(h.LogicalPath, filepath.FromSlash(rel)), CanonicalPath: resolved, Accessible: true}, nil
}

func (p *Port) Destination(ctx context.Context, r effects.RootInput, rel string) (credentials.LinkObservation, error) {
	if !p.Supported() {
		return credentials.LinkObservation{}, ErrUnsupported
	}
	if credentials.ValidateDestination(rel) != nil || ctx.Err() != nil {
		return credentials.LinkObservation{}, ErrDestination
	}
	if _, e := os.Lstat(r.Path); errors.Is(e, fs.ErrNotExist) {
		// Preflight can precede root/Mat candidate creation. This is observational
		// only; OpenCandidate must prove the actual root before any effect.
		resolved, e := pathsafe.ResolveUnder(r.AllowedBase, strings.TrimPrefix(r.Path, r.AllowedBase+string(filepath.Separator)))
		if e != nil || !contained(r.MutationIdentity, resolved) {
			return credentials.LinkObservation{}, ErrCustody
		}
		return credentials.LinkObservation{}, nil
	}
	s, e := p.OpenCandidate(ctx, r)
	if e != nil {
		return credentials.LinkObservation{}, e
	}
	defer s.Close()
	// During preflight Mat may still need to create declared parent directories.
	// Every existing component must already be safe; Apply requires all parents.
	if concrete, ok := s.(*session); ok {
		dir := filepath.Dir(filepath.FromSlash(rel))
		walk := ""
		if dir != "." {
			for _, part := range strings.Split(dir, string(filepath.Separator)) {
				walk = filepath.Join(walk, part)
				st, err := concrete.root.Lstat(walk)
				if errors.Is(err, fs.ErrNotExist) {
					return credentials.LinkObservation{}, nil
				}
				if err != nil || !safeDirectory(st) {
					return credentials.LinkObservation{}, ErrCustody
				}
			}
		}
	}
	return s.Inspect(ctx, rel)
}

func (p *Port) OpenCandidate(ctx context.Context, r effects.RootInput) (credentials.CandidateSession, error) {
	if !p.Supported() {
		return nil, ErrUnsupported
	}
	if !r.Inactive || !r.PrivateCustody || ctx.Err() != nil || !filepath.IsAbs(r.Path) || r.MutationIdentity == "" {
		return nil, ErrCustody
	}
	st, e := os.Lstat(r.Path)
	if e != nil || !privateDirectory(st) {
		return nil, ErrCustody
	}
	real, e := filepath.EvalSymlinks(r.Path)
	if e != nil || !contained(r.MutationIdentity, real) || real == r.MutationIdentity {
		return nil, ErrCustody
	}
	base, e := filepath.EvalSymlinks(r.AllowedBase)
	if e != nil || !contained(base, real) || base == real {
		return nil, ErrCustody
	}
	root, e := os.OpenRoot(r.Path)
	if e != nil {
		return nil, ErrCustody
	}
	s := &session{root: root, input: r, info: st, parents: map[string]*os.Root{}}
	if s.Validate(ctx) != nil {
		root.Close()
		return nil, ErrCustody
	}
	return s, nil
}

type session struct {
	root    *os.Root
	input   effects.RootInput
	info    fs.FileInfo
	parents map[string]*os.Root
	closed  bool
}

func (s *session) Validate(ctx context.Context) error {
	if s.closed || ctx.Err() != nil || !s.input.PrivateCustody || !s.input.Inactive {
		return ErrCustody
	}
	st, e := os.Lstat(s.input.Path)
	if e != nil || !privateDirectory(st) || !os.SameFile(s.info, st) {
		return ErrCustody
	}
	pinned, e := s.root.Stat(".")
	if e != nil || !os.SameFile(s.info, pinned) {
		return ErrCustody
	}
	real, e := filepath.EvalSymlinks(s.input.Path)
	if e != nil || !contained(s.input.MutationIdentity, real) {
		return ErrCustody
	}
	return nil
}

func (s *session) parent(ctx context.Context, rel string) (*os.Root, string, string, error) {
	if credentials.ValidateDestination(rel) != nil || s.Validate(ctx) != nil {
		return nil, "", "", ErrCustody
	}
	dir := filepath.Dir(filepath.FromSlash(rel))
	walk := ""
	if dir != "." {
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			walk = filepath.Join(walk, part)
			st, e := s.root.Lstat(walk)
			if e != nil || !safeDirectory(st) {
				return nil, "", "", ErrCustody
			}
		}
	}
	p := s.parents[dir]
	if p == nil {
		var e error
		p, e = s.root.OpenRoot(dir)
		if e != nil {
			return nil, "", "", ErrDestination
		}
		s.parents[dir] = p
	}
	st, e := p.Stat(".")
	if e != nil {
		return nil, "", "", ErrCustody
	}
	live, e := s.root.Lstat(dir)
	if e != nil || !safeDirectory(live) || !os.SameFile(st, live) {
		return nil, "", "", ErrCustody
	}
	return p, filepath.Base(rel), identity(st), nil
}

func (s *session) Inspect(ctx context.Context, rel string) (credentials.LinkObservation, error) {
	p, name, parentID, e := s.parent(ctx, rel)
	if e != nil {
		return credentials.LinkObservation{}, e
	}
	st, e := p.Lstat(name)
	if errors.Is(e, fs.ErrNotExist) {
		return credentials.LinkObservation{ParentIdentity: parentID}, nil
	}
	if e != nil {
		return credentials.LinkObservation{}, ErrDestination
	}
	o := credentials.LinkObservation{Exists: true, IsLink: st.Mode()&fs.ModeSymlink != 0, Identity: identity(st), ParentIdentity: parentID}
	if o.IsLink {
		o.Target, e = p.Readlink(name)
		if e != nil {
			return credentials.LinkObservation{}, ErrDestination
		}
		after, e := p.Lstat(name)
		if e != nil || !os.SameFile(st, after) {
			return credentials.LinkObservation{}, ErrDestination
		}
	}
	return o, nil
}

func (s *session) CreateExclusive(ctx context.Context, source, rel string) (credentials.LinkObservation, error) {
	if !filepath.IsAbs(source) || strings.ContainsRune(source, 0) {
		return credentials.LinkObservation{}, ErrSource
	}
	p, name, parentID, e := s.parent(ctx, rel)
	if e != nil {
		return credentials.LinkObservation{}, e
	}
	// Symlink never replaces an existing destination. The target intentionally
	// points outside the candidate at the authorized logical resource path.
	if p.Symlink(source, name) != nil {
		return credentials.LinkObservation{}, ErrDestination
	}
	o, e := s.Inspect(ctx, rel)
	if e != nil {
		return credentials.LinkObservation{Exists: true, IsLink: true, Target: source, ParentIdentity: parentID}, e
	}
	return o, nil
}

func (s *session) RemoveIfMatches(ctx context.Context, rel string, want credentials.LinkObservation) error {
	if !want.Exists || !want.IsLink || want.Identity == "" || want.ParentIdentity == "" {
		return ErrCustody
	}
	got, e := s.Inspect(ctx, rel)
	if e != nil || got != want {
		return ErrCustody
	}
	p, name, parentID, e := s.parent(ctx, rel)
	if e != nil || parentID != want.ParentIdentity {
		return ErrCustody
	}
	// Private custody and cooperating locks are required. This is deliberately
	// not advertised as atomic compare-and-unlink against an adversarial writer.
	if p.Remove(name) != nil {
		return ErrDestination
	}
	return nil
}

func (s *session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for _, p := range s.parents {
		if e := p.Close(); e != nil {
			errs = append(errs, ErrCustody)
		}
	}
	if e := s.root.Close(); e != nil {
		errs = append(errs, ErrCustody)
	}
	return errors.Join(errs...)
}

var _ credentials.LinkPort = (*Port)(nil)
