package materialize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

type InstalledPhase string

const (
	InstalledPrepared  InstalledPhase = "prepared"
	InstalledIntent    InstalledPhase = "intent"
	InstalledPublished InstalledPhase = "published"
	InstalledVerified  InstalledPhase = "verified"
)

type CaseMode string

const (
	CaseSensitive   CaseMode = "sensitive"
	CaseInsensitive CaseMode = "insensitive"
)
const InstalledFileLimit = 16 << 20

// InstalledMetadata is content-free descriptor evidence. Unknown enumeration is
// never represented by an empty successful observation.
type InstalledMetadata struct {
	UID, GID, FullMode      uint32
	Links                   uint64
	Flags                   uint32
	Volume                  string
	Complete                bool
	ACLAbsent, XattrsAbsent bool
}

// InstalledCapabilities binds backend semantics to a particular filesystem and
// pinned root, rather than treating a case-mode string as filesystem authority.
type InstalledCapabilities struct {
	Volume, RootIdentity, Revision, ObservationRevision string
	CaseMode                                            CaseMode
	Creation                                            InstalledMetadata
	RootMetadata                                        InstalledMetadata
}

func (c InstalledCapabilities) Valid() bool {
	return c.Volume != "" && c.RootIdentity != "" && c.Revision == installedMetadataRevision && c.ObservationRevision != "" && (c.CaseMode == CaseSensitive || c.CaseMode == CaseInsensitive) && c.Creation.Complete && c.Creation.ACLAbsent && c.Creation.XattrsAbsent && c.Creation.Flags == 0 && c.Creation.Volume == c.Volume && c.Creation.FullMode == 0 && c.Creation.Links == 0 && allowedInstalledMetadata(c.RootMetadata, true) && c.RootMetadata.Volume == c.Volume
}

const installedMetadataRevision = "workspace.installed.metadata.v1"

func allowedInstalledMetadata(m InstalledMetadata, directory bool) bool {
	return m.Complete && m.ACLAbsent && m.XattrsAbsent && m.Volume != "" && m.Flags == 0 && m.FullMode & ^uint32(0777) == 0 && (directory || m.Links == 1)
}

func (m InstalledMetadata) PreservableDirectory() bool { return allowedInstalledMetadata(m, true) }
func (m InstalledMetadata) PreservableFile(creation InstalledMetadata, mode uint32) bool {
	return allowedInstalledMetadata(m, false) && m.UID == creation.UID && m.GID == creation.GID && m.FullMode == mode && m.Volume == creation.Volume
}

// InspectInstalledCapabilities performs no creation, chmod or metadata copying.
func InspectInstalledCapabilities(ctx context.Context, target string, mode CaseMode) (InstalledCapabilities, error) {
	if ctx == nil {
		return InstalledCapabilities{}, ErrUnsafeTarget
	}
	if err := ctx.Err(); err != nil {
		return InstalledCapabilities{}, err
	}
	root, _, err := openExistingTargetRoot(target)
	if err != nil {
		return InstalledCapabilities{}, ErrUnsafeTarget
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return InstalledCapabilities{}, ErrUnsafeTarget
	}
	defer f.Close()
	m, id, err := installedMetadata(f, true)
	if err != nil {
		return InstalledCapabilities{}, err
	}
	if !allowedInstalledMetadata(m, true) || mode != CaseSensitive && mode != CaseInsensitive {
		return InstalledCapabilities{}, ErrUnsupportedOperation
	}
	creation := m
	creation.UID, creation.GID = installedCreator()
	creation.FullMode = 0
	creation.Links = 0
	c := InstalledCapabilities{Volume: m.Volume, RootIdentity: id, Revision: installedMetadataRevision, CaseMode: mode, Creation: creation, RootMetadata: m}
	// This revision identifies the observed descriptor/volume/profile tuple;
	// protocol revision and freshness windows remain distinct fields.
	raw, _ := json.Marshal(c)
	c.ObservationRevision = artifact.DigestBytes(raw).Hex
	if !c.Valid() {
		return InstalledCapabilities{}, ErrUnsupportedOperation
	}
	return c, nil
}

type InstalledSnapshot struct {
	Metadata InstalledMetadata
	Creation InstalledMetadata
	Path     string
	Exists   bool
	Kind     artifact.EntryKind
	Mode     uint32
	Identity string
	Bytes    []byte
	Digest   artifact.Digest
	Parents  []InstalledDirectoryChange
}

// InstalledDirectoryChange records traversal observations and exact directories
// created by this operation. Existing directories confer no ownership.
type InstalledDirectoryChange struct {
	Metadata        InstalledMetadata
	Path, Identity  string
	Mode            uint32
	Exists, Created bool
	Phase           InstalledPhase
}
type InstalledTemp struct {
	Metadata       InstalledMetadata
	Path, Identity string
	Phase          InstalledPhase
}
type InstalledFileChange struct {
	Path, BeforeIdentity, AfterIdentity string
	BeforeMetadata, AfterMetadata       InstalledMetadata
	BeforeExists                        bool
	BeforeMode, AfterMode               uint32
	Before, After                       artifact.Digest
	GrantID, GrantVersion               string
	KeysBefore, KeysAfter               []keymerge.KeyState
	Phase                               InstalledPhase
	Stage                               InstalledTemp
}
type InstalledPolicy struct {
	Capabilities                    InstalledCapabilities
	TargetIdentity, ControlIdentity string
	CaseMode                        CaseMode
	Stage                           InstalledTemp
	Directories                     []InstalledDirectoryChange
	Files                           []InstalledFileChange
}

// InspectInstalled reads only selected regular leaves. It validates parent and
// leaf types before open, uses nonblocking/no-follow file opens, bounds reads,
// and rejects Unicode/platform-case aliases without rewriting path spellings.
// CaseMode is explicit host-attested filesystem behavior, never inferred from OS.
func InspectInstalled(ctx context.Context, target string, tree artifact.Tree, mode CaseMode) ([]InstalledSnapshot, error) {
	if ctx == nil {
		return nil, ErrUnsafeTarget
	}
	if mode != CaseSensitive && mode != CaseInsensitive {
		return nil, ErrUnsupportedOperation
	}
	entries, err := artifact.Normalize(tree.Entries)
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	root, _, err := openExistingTargetRoot(target)
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	defer root.Close()
	if err = checkInstalledAliases(ctx, root, entries, mode); err != nil {
		return nil, err
	}
	var result []InstalledSnapshot
	for _, e := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		s, err := inspectInstalledLeaf(root, e.Path)
		if err != nil {
			return nil, err
		}
		if e.Kind == artifact.EntryDirectory && s.Exists && s.Kind != artifact.EntryDirectory {
			return nil, ErrConflict
		}
		if e.Kind == artifact.EntryFile && s.Exists && s.Kind != artifact.EntryFile {
			return nil, ErrConflict
		}
		if e.Kind == artifact.EntryDirectory {
			s.Parents = append(s.Parents, InstalledDirectoryChange{Path: e.Path, Exists: s.Exists, Identity: s.Identity, Mode: s.Mode, Metadata: s.Metadata})
		}
		result = append(result, s)
	}
	return result, nil
}
func inspectInstalledLeaf(root *os.Root, rel string) (InstalledSnapshot, error) {
	s := InstalledSnapshot{Path: rel}
	parent, err := root.Open(".")
	if err != nil {
		return s, ErrUnsafeTarget
	}
	pm, _, err := installedMetadata(parent, true)
	parent.Close()
	if err != nil {
		return s, err
	}
	if !allowedInstalledMetadata(pm, true) {
		return s, ErrUnsupportedOperation
	}
	pm.UID, pm.GID = installedCreator()
	pm.FullMode = 0
	pm.Links = 0
	s.Creation = pm
	parts := strings.Split(rel, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			for j := i; j < len(parts)-1; j++ {
				s.Parents = append(s.Parents, InstalledDirectoryChange{Path: strings.Join(parts[:j+1], "/")})
			}
			return s, nil
		}
		if err != nil {
			return s, ErrUnsafeTarget
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return s, ErrUnsafeTarget
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return s, ErrConflict
			}
			id := installedIdentity(info)
			if id == "" {
				return s, ErrUnsupportedOperation
			}
			f, er := openInstalledRegular(root, p)
			if er != nil {
				return s, ErrUnsafeTarget
			}
			dm, di, er := installedMetadata(f, true)
			f.Close()
			if er != nil {
				return s, er
			}
			if di != id {
				return s, ErrConflict
			}
			if !allowedInstalledMetadata(dm, true) {
				return s, ErrUnsupportedOperation
			}
			dm.Links = 0
			s.Parents = append(s.Parents, InstalledDirectoryChange{Path: p, Exists: true, Identity: id, Mode: uint32(info.Mode().Perm()), Metadata: dm})
			creation := dm
			creation.UID, creation.GID = installedCreator()
			creation.FullMode = 0
			s.Creation = creation
			continue
		}
		s.Exists = true
		s.Mode = uint32(info.Mode().Perm())
		s.Identity = installedIdentity(info)
		if s.Identity == "" {
			return s, ErrUnsupportedOperation
		}
		if info.IsDir() {
			s.Kind = artifact.EntryDirectory
			f, er := openInstalledRegular(root, p)
			if er != nil {
				return s, ErrUnsafeTarget
			}
			m, id, er := installedMetadata(f, true)
			f.Close()
			if er != nil {
				return s, er
			}
			if id != s.Identity {
				return s, ErrConflict
			}
			if !allowedInstalledMetadata(m, true) {
				return s, ErrUnsupportedOperation
			}
			m.Links = 0
			s.Metadata = m
			return s, nil
		}
		if !info.Mode().IsRegular() || info.Size() > InstalledFileLimit {
			return s, ErrUnsafeTarget
		}
		s.Kind = artifact.EntryFile
		f, err := openInstalledRegular(root, p)
		if err != nil {
			return s, ErrUnsafeTarget
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
			f.Close()
			return s, ErrConflict
		}
		s.Metadata, _, err = installedMetadata(f, false)
		if err != nil || !allowedInstalledMetadata(s.Metadata, false) {
			f.Close()
			return s, ErrUnsupportedOperation
		}
		s.Bytes, err = io.ReadAll(io.LimitReader(f, InstalledFileLimit+1))
		second, _, me := installedMetadata(f, false)
		ce := f.Close()
		if me != nil || second != s.Metadata {
			return s, ErrConflict
		}
		if err != nil || ce != nil || len(s.Bytes) > InstalledFileLimit {
			return s, ErrUnsafeTarget
		}
		after, err := root.Lstat(p)
		if err != nil || !os.SameFile(info, after) || after.Mode() != info.Mode() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return s, ErrConflict
		}
		s.Digest = artifact.DigestBytes(s.Bytes)
	}
	return s, nil
}
func aliasKey(s string, mode CaseMode) string {
	s = norm.NFC.String(s)
	if mode == CaseInsensitive {
		s = norm.NFC.String(cases.Fold().String(s))
	}
	return s
}
func checkInstalledAliases(ctx context.Context, root *os.Root, entries []artifact.Entry, mode CaseMode) error {
	proposed := map[string]string{}
	parents := map[string]bool{}
	for _, e := range entries {
		if !utf8.ValidString(e.Path) {
			return ErrUnsafeTarget
		}
		parts := strings.Split(e.Path, "/")
		for i := range parts {
			rel := strings.Join(parts[:i+1], "/")
			k := aliasKey(rel, mode)
			if prior, ok := proposed[k]; ok && prior != rel {
				return ErrConflict
			}
			proposed[k] = rel
			parents[path.Dir(rel)] = true
		}
	}
	dirs := make([]string, 0, len(parents))
	for d := range parents {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d != "." {
			parts := strings.Split(d, "/")
			for i := range parts {
				p := strings.Join(parts[:i+1], "/")
				info, er := root.Lstat(p)
				if errors.Is(er, fs.ErrNotExist) {
					break
				}
				if er != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return ErrUnsafeTarget
				}
			}
		}
		var f *os.File
		var err error
		if d == "." {
			f, err = root.Open(".")
		} else {
			f, err = openInstalledRegular(root, d)
		}
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return ErrUnsafeTarget
		}
		names, err := f.Readdirnames(4097)
		ce := f.Close()
		if err != nil && err != io.EOF || ce != nil || len(names) > 4096 {
			return ErrUnsupportedOperation
		}
		seen := map[string]string{}
		for _, n := range names {
			if !utf8.ValidString(n) {
				return ErrUnsupportedOperation
			}
			rel := path.Join(d, n)
			k := aliasKey(rel, mode)
			if p, ok := seen[k]; ok && p != rel {
				return ErrConflict
			}
			seen[k] = rel
			if p, ok := proposed[k]; ok && p != rel {
				return ErrConflict
			}
		}
	}
	return nil
}

func (e *DefaultEngine) planInstalled(ctx context.Context, req Request) (Plan, error) {
	if ctx == nil {
		return Plan{}, ErrUnsafeTarget
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if req.Installed == nil || req.Generation == "" || req.Installed.TargetIdentity == "" || req.Installed.ControlIdentity == "" {
		return Plan{}, ErrUnsafeTarget
	}
	cap, err := InspectInstalledCapabilities(ctx, req.TargetRoot, req.Installed.CaseMode)
	if err != nil || !req.Installed.Capabilities.Valid() {
		return Plan{}, ErrUnsupportedOperation
	}
	if cap != req.Installed.Capabilities {
		return Plan{}, ErrConflict
	}
	if req.Installed.Stage.Phase != "" && req.Installed.Stage.Phase != InstalledPrepared || req.Installed.Stage.Identity != "" {
		return Plan{}, ErrConflict
	}
	for _, d := range req.Installed.Directories {
		if d.Created || d.Phase != "" && d.Phase != InstalledPrepared {
			return Plan{}, ErrConflict
		}
	}
	entries, err := artifact.Normalize(req.Artifacts.Entries)
	if err != nil {
		return Plan{}, ErrUnsafeTarget
	}
	snapshots, err := InspectInstalled(ctx, req.TargetRoot, artifact.Tree{Entries: entries}, req.Installed.CaseMode)
	if err != nil {
		return Plan{}, err
	}
	info, err := os.Lstat(req.TargetRoot)
	if err != nil || installedIdentity(info) != req.Installed.TargetIdentity {
		return Plan{}, ErrConflict
	}
	changes := map[string]InstalledFileChange{}
	for _, c := range req.Installed.Files {
		if _, ok := changes[c.Path]; ok {
			return Plan{}, ErrConflict
		}
		changes[c.Path] = c
	}
	report := Report{Operation: OperationInstall, Complete: true}
	var manifest []artifact.Entry
	for i, entry := range entries {
		before := snapshots[i]
		if entry.Kind == artifact.EntryDirectory {
			continue
		} // never adopt/chmod operator dirs
		c, ok := changes[entry.Path]
		if !ok || c.Phase != InstalledPrepared || c.GrantID == "" || c.GrantVersion == "" || c.After != artifact.DigestBytes(entry.Bytes) || c.AfterMode != uint32(modeFor(entry)) {
			return Plan{}, ErrConflict
		}
		if !before.Creation.Complete || before.Creation != cap.Creation || c.AfterMetadata != expectedInstalledMetadata(cap.Creation, c.AfterMode) || before.Exists && !before.Metadata.PreservableFile(cap.Creation, c.AfterMode) {
			return Plan{}, ErrUnsupportedOperation
		}
		if c.BeforeMetadata != before.Metadata {
			return Plan{}, ErrConflict
		}
		if c.BeforeExists != before.Exists || c.BeforeIdentity != before.Identity || c.BeforeMode != before.Mode || c.Before != before.Digest {
			return Plan{}, ErrConflict
		}
		delete(changes, entry.Path)
		manifest = append(manifest, entry)
		kind := ChangeCreate
		if before.Exists {
			kind = ChangeUpdate
		}
		if before.Exists && before.Digest == c.After && before.Mode == c.AfterMode {
			kind = ChangeUnchanged
		}
		report.Changes = append(report.Changes, Change{Path: entry.Path, Kind: kind, Before: c.Before, After: c.After, EntryID: entry.Ownership.EntryID, GroupID: entry.Ownership.GroupID})
	}
	if len(req.Installed.Directories) > 0 {
		if err := checkInstalledDirectories(snapshots, req.Installed.Directories); err != nil {
			return Plan{}, err
		}
	}
	if len(changes) > 0 {
		return Plan{}, ErrConflict
	}
	return Plan{Request: req, Manifest: buildManifest(req, manifest, e.now()), Report: report, WillMutate: len(report.Changes) > 0}, nil
}
func (e *DefaultEngine) applyInstalled(ctx context.Context, req Request) (h Handle, err error) {
	plan, err := e.planInstalled(ctx, req)
	if err != nil {
		return h, err
	}
	h = Handle{TargetRoot: req.TargetRoot, Manifest: plan.Manifest, Report: plan.Report}
	h.Report.Complete = false
	root, _, err := openExistingTargetRoot(req.TargetRoot)
	if err != nil {
		return h, ErrUnsafeTarget
	}
	defer root.Close()
	entries, _ := artifact.Normalize(req.Artifacts.Entries)
	frozen, err := InspectInstalled(ctx, req.TargetRoot, artifact.Tree{Entries: entries}, req.Installed.CaseMode)
	if err != nil {
		return h, err
	}
	directories := installedDirectories(frozen)
	if len(req.Installed.Directories) > 0 {
		directories = slices.Clone(req.Installed.Directories)
	}
	h.InstalledDirectories = slices.Clone(directories)
	// The engine creates only its own private stage after aggregate preflight and
	// the caller's durable aggregate intent. Failures retain it, never RemoveAll.
	stage := req.Installed.Stage.Path
	if stage == "" {
		stage = ".installed-stage-" + randomSuffix()
	}
	if artifact.ValidateRelPath(stage) != nil || strings.Contains(stage, "/") || !strings.HasPrefix(stage, ".installed-stage-") {
		return h, ErrUnsafeTarget
	}
	if _, er := root.Lstat(stage); !errors.Is(er, fs.ErrNotExist) {
		return h, ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return h, err
	}
	if err = root.Mkdir(stage, 0700); err != nil {
		return h, ErrUnsafeTarget
	}
	h.Mutated = true
	h.Retained = append(h.Retained, stage)
	stageInfo, er := root.Lstat(stage)
	if er != nil {
		return h, ErrUnsafeTarget
	}
	h.InstalledStage = InstalledTemp{Path: stage, Identity: installedIdentity(stageInfo), Phase: InstalledIntent}
	stageFile, er := openInstalledRegular(root, stage)
	if er != nil {
		return h, ErrUnsafeTarget
	}
	stageMeta, _, er := installedMetadata(stageFile, true)
	stageFile.Close()
	if er != nil || stageMeta != expectedInstalledDirectoryMetadata(req.Installed.Capabilities.Creation, 0700) {
		return h, ErrUnsupportedOperation
	}
	h.InstalledStage = InstalledTemp{Metadata: stageMeta, Path: stage, Identity: installedIdentity(stageInfo), Phase: InstalledIntent}
	if h.InstalledStage.Identity == "" {
		return h, ErrUnsupportedOperation
	}
	for _, entry := range entries {
		if entry.Kind == artifact.EntryDirectory {
			continue
		}
		var c InstalledFileChange
		for _, change := range req.Installed.Files {
			if change.Path == entry.Path {
				c = change
				break
			}
		}
		if err = ctx.Err(); err != nil {
			return h, err
		}
		before, er := inspectInstalledLeaf(root, entry.Path)
		if er != nil {
			return h, er
		}
		if !matchesInstalledBefore(c, before) {
			return h, ErrConflict
		}
		tmp := path.Join(stage, fmt.Sprintf("file-%d", len(h.Installed)))
		f, er := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(c.AfterMode))
		if er != nil {
			return h, ErrUnsafeTarget
		}
		// Observe creation metadata before writing or changing any mode. An
		// unexpected inherited ACL/xattr/owner is retained, never repaired away.
		created, _, me := installedMetadata(f, false)
		if me != nil || created != c.AfterMetadata {
			f.Close()
			return h, ErrUnsupportedOperation
		}
		_, er = f.Write(entry.Bytes)
		ce := f.Close()
		if er != nil || ce != nil {
			return h, ErrUnsafeTarget
		}
		staged, er := inspectInstalledLeaf(root, tmp)
		if er != nil || staged.Digest != c.After || staged.Mode != c.AfterMode || staged.Metadata != c.AfterMetadata {
			return h, ErrConflict
		}
		c.Phase = InstalledIntent
		c.Stage = h.InstalledStage
		if e.opts.BeforeInstalledCommit != nil {
			if err = e.opts.BeforeInstalledCommit(ctx, c); err != nil {
				return h, err
			}
		}
		if err = ctx.Err(); err != nil {
			return h, err
		}
		// Reobserve root, stage, traversal and bytes AFTER every callback.
		if er = validateInstalledRoot(req); er != nil {
			return h, er
		}
		stageFD, er := openInstalledRegular(root, stage)
		if er != nil {
			return h, ErrConflict
		}
		stageNow, _, er := installedMetadata(stageFD, true)
		stageFD.Close()
		if er != nil || stageNow != h.InstalledStage.Metadata {
			return h, ErrConflict
		}
		currentStage, er := root.Lstat(stage)
		if er != nil || !os.SameFile(stageInfo, currentStage) || !currentStage.IsDir() || currentStage.Mode().Perm() != 0700 {
			return h, ErrConflict
		}
		currentTmp, er := inspectInstalledLeaf(root, tmp)
		if er != nil || currentTmp.Identity != staged.Identity || currentTmp.Digest != staged.Digest || currentTmp.Mode != staged.Mode || currentTmp.Metadata != staged.Metadata {
			return h, ErrConflict
		}
		fresh, er := InspectInstalled(ctx, req.TargetRoot, artifact.Tree{Entries: entries}, req.Installed.CaseMode)
		if er != nil {
			return h, er
		}
		if er = checkInstalledDirectories(fresh, h.InstalledDirectories); er != nil {
			return h, er
		}
		// Callbacks can revoke authority or edit a leaf. Reinspect after them.
		before, er = inspectInstalledLeaf(root, entry.Path)
		if er != nil {
			return h, er
		}
		if !matchesInstalledBefore(c, before) {
			return h, ErrConflict
		}
		if er = checkInstalledAliases(ctx, root, entries, req.Installed.CaseMode); er != nil {
			return h, er
		}
		parent := path.Dir(entry.Path)
		if parent != "." {
			for _, d := range h.InstalledDirectories {
				if d.Exists || d.Created || !(d.Path == parent || strings.HasPrefix(parent, d.Path+"/")) {
					continue
				}
				if err = ctx.Err(); err != nil {
					return h, err
				}
				if err = root.Mkdir(d.Path, 0755); err != nil {
					return h, ErrUnsafeTarget
				}
				info, er := root.Lstat(d.Path)
				if er != nil || !info.IsDir() {
					return h, ErrUnsafeTarget
				}
				for j := range h.InstalledDirectories {
					if h.InstalledDirectories[j].Path == d.Path {
						h.InstalledDirectories[j].Exists = true
						h.InstalledDirectories[j].Created = true
						h.InstalledDirectories[j].Identity = installedIdentity(info)
						h.InstalledDirectories[j].Mode = uint32(info.Mode().Perm())
						h.InstalledDirectories[j].Phase = InstalledPublished
					}
				}
				df, me := openInstalledRegular(root, d.Path)
				if me != nil {
					return h, ErrConflict
				}
				dm, _, me := installedMetadata(df, true)
				df.Close()
				if me != nil || dm != expectedInstalledDirectoryMetadata(req.Installed.Capabilities.Creation, 0755) {
					return h, ErrUnsupportedOperation
				}
				for j := range h.InstalledDirectories {
					if h.InstalledDirectories[j].Path == d.Path {
						h.InstalledDirectories[j].Exists = true
						h.InstalledDirectories[j].Created = true
						h.InstalledDirectories[j].Identity = installedIdentity(info)
						h.InstalledDirectories[j].Mode = uint32(info.Mode().Perm())
						h.InstalledDirectories[j].Metadata = dm
						h.InstalledDirectories[j].Phase = InstalledPublished
					}
				}
			}
		}
		if err = ctx.Err(); err != nil {
			return h, err
		}
		// Creating parents is mutation too. Revalidate the complete traversal and
		// staged descriptor metadata immediately before destination publication.
		if er = validateInstalledRoot(req); er != nil {
			return h, er
		}
		if _, er = inspectInstalledLeaf(root, entry.Path); er != nil {
			return h, er
		}
		stagedNow, er := inspectInstalledLeaf(root, tmp)
		if er != nil || stagedNow.Metadata != c.AfterMetadata || stagedNow.Identity != staged.Identity || stagedNow.Digest != c.After {
			return h, ErrConflict
		}
		if err = root.Rename(tmp, entry.Path); err != nil {
			return h, ErrUnsafeTarget
		}
		c.Phase = InstalledPublished
		h.Installed = append(h.Installed, c)
		after, er := inspectInstalledLeaf(root, entry.Path)
		if er != nil || after.Digest != c.After || after.Mode != c.AfterMode || after.Metadata != c.AfterMetadata {
			return h, ErrConflict
		}
		h.Installed[len(h.Installed)-1].Phase = InstalledVerified
		h.Installed[len(h.Installed)-1].AfterIdentity = after.Identity
	}
	// Remove ONLY this operation's empty stage, never a user directory or a
	// nonempty/uncertain stage. Root policy controls later retained recovery.
	if err = validateInstalledRoot(req); err != nil {
		return h, err
	}
	stageFD, er := openInstalledRegular(root, stage)
	if er != nil {
		return h, ErrConflict
	}
	stageNow, _, er := installedMetadata(stageFD, true)
	stageFD.Close()
	if er != nil || stageNow != h.InstalledStage.Metadata {
		return h, ErrConflict
	}
	currentStage, er := root.Lstat(stage)
	if er != nil || !os.SameFile(stageInfo, currentStage) || !currentStage.IsDir() || currentStage.Mode().Perm() != 0700 {
		return h, ErrConflict
	}
	if err = root.Remove(stage); err != nil {
		return h, ErrUnsafeTarget
	}
	h.Retained = nil
	h.InstalledStage.Phase = InstalledVerified
	for j := range h.InstalledDirectories {
		if h.InstalledDirectories[j].Created {
			h.InstalledDirectories[j].Phase = InstalledVerified
		}
	}
	h.Report.Complete = true
	return h, nil
}
func matchesInstalledBefore(c InstalledFileChange, s InstalledSnapshot) bool {
	return c.BeforeExists == s.Exists && c.BeforeIdentity == s.Identity && c.BeforeMode == s.Mode && c.BeforeMetadata == s.Metadata && reflect.DeepEqual(c.Before, s.Digest)
}

func expectedInstalledMetadata(creation InstalledMetadata, mode uint32) InstalledMetadata {
	m := creation
	m.FullMode = mode
	m.Links = 1
	return m
}
func expectedInstalledDirectoryMetadata(creation InstalledMetadata, mode uint32) InstalledMetadata {
	m := creation
	m.FullMode = mode
	m.Links = 0
	return m
}
func validateInstalledRoot(req Request) error {
	cap, err := InspectInstalledCapabilities(context.Background(), req.TargetRoot, req.Installed.CaseMode)
	if err != nil {
		return err
	}
	if cap != req.Installed.Capabilities {
		return ErrConflict
	}
	info, err := os.Lstat(req.TargetRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || installedIdentity(info) != req.Installed.TargetIdentity {
		return ErrConflict
	}
	return nil
}
func installedDirectories(snapshots []InstalledSnapshot) []InstalledDirectoryChange {
	dirs := map[string]InstalledDirectoryChange{}
	for _, s := range snapshots {
		for _, d := range s.Parents {
			dirs[d.Path] = d
		}
		if s.Kind == artifact.EntryDirectory {
			dirs[s.Path] = InstalledDirectoryChange{Path: s.Path, Identity: s.Identity, Mode: s.Mode, Exists: s.Exists, Metadata: s.Metadata}
		}
	}
	var result []InstalledDirectoryChange
	for _, d := range dirs {
		result = append(result, d)
	}
	slices.SortFunc(result, func(a, b InstalledDirectoryChange) int { return strings.Compare(a.Path, b.Path) })
	return result
}
func checkInstalledDirectories(snapshots []InstalledSnapshot, expected []InstalledDirectoryChange) error {
	actual := installedDirectories(snapshots)
	if len(actual) != len(expected) {
		return ErrConflict
	}
	for i, d := range actual {
		e := expected[i]
		if d.Path != e.Path || d.Exists != e.Exists || d.Identity != e.Identity || d.Mode != e.Mode || d.Metadata != e.Metadata {
			return ErrConflict
		}
	}
	return nil
}
