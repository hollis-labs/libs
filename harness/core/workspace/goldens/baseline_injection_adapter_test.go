// Historical baseline adapter: test-only, with writes delegated to the sole engine.
package goldens_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	agentlaunch "github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

var (
	baselineErrUnsafePath            = errors.New("bootdir: unsafe bootdir-relative path")
	baselineErrUnsupportedNativeKind = errors.New("bootdir: unsupported native file kind")
	baselineErrEmptyBootDir          = errors.New("bootdir: empty boot dir")
	baselineErrEmptyRelPath          = errors.New("bootdir: empty relative path")
)

const (
	baselineSourceNative  = "native"
	baselineSourceOverlay = "overlay"
	baselineSourceFile    = "file"
)

type baselineFile struct {
	RelPath string
	Content string
	Mode    fs.FileMode
}

type baselineDryRunPlan struct {
	Files       []baselinePlannedFile
	NativeFiles []agentlaunch.NativeFile
	Overlays    []baselineFile
}

type baselinePlannedFile struct {
	RelPath string
	Mode    fs.FileMode
	Source  string
}

type baselineWrittenFile struct {
	RelPath string
	Mode    fs.FileMode
	Source  string
}

type baselineWriteResult struct {
	Files []baselineWrittenFile
}

type baselineNativeResolver func(agentlaunch.NativeFile) (baselineFile, error)

type baselineWriter struct {
	Authorize agentlaunch.ArtifactAuthorizer
	// OnWritten observes completed file metadata; it is not a writer port.
	OnWritten func(baselineWrittenFile)
}

type baselineWriteOptions struct {
	// OverlayWinsLast is the default and only currently supported ordering:
	// native files write first, overlays write last in sorted path order.
	OverlayWinsLast bool

	// baselineNativeResolver resolves non-baselineRaw native file kinds. When nil, only
	// agentlaunch.NativeFileRaw is accepted.
	baselineNativeResolver baselineNativeResolver
}

type baselineRequest struct {
	Provider    string
	Runtime     runtimes.Mode
	NativeFiles []agentlaunch.NativeFile
	Overlays    map[string]string
}

// baselineBuildInjection validates caller-supplied native files and overlays and
// returns the go-agent-launch InjectionSpec. Apps own the content renderers;
// this package owns the safety boundary and dry-run shape.
func baselineBuildInjection(req baselineRequest) (agentlaunch.InjectionSpec, baselineDryRunPlan, error) {
	out := agentlaunch.InjectionSpec{
		NativeFiles:    append([]agentlaunch.NativeFile(nil), req.NativeFiles...),
		BootDirOverlay: map[string]string{},
	}
	plan := baselineDryRunPlan{NativeFiles: append([]agentlaunch.NativeFile(nil), req.NativeFiles...)}
	for i := range out.NativeFiles {
		if err := out.NativeFiles[i].Validate(); err != nil {
			return agentlaunch.InjectionSpec{}, baselineDryRunPlan{}, err
		}
	}
	for rel, content := range req.Overlays {
		if err := baselineValidateRelPath(rel); err != nil {
			return agentlaunch.InjectionSpec{}, baselineDryRunPlan{}, fmt.Errorf("%w: %s", err, rel)
		}
		out.BootDirOverlay[rel] = content
		plan.Overlays = append(plan.Overlays, baselineFile{RelPath: rel, Content: content, Mode: 0o644})
	}
	if len(out.BootDirOverlay) == 0 {
		out.BootDirOverlay = nil
	}
	return out, plan, nil
}

func baselineValidateRelPath(rel string) error {
	if rel == "" {
		return baselineErrEmptyRelPath
	}
	if err := agentlaunch.ValidateBootDirRelPath(rel); err != nil {
		return baselineErrUnsafePath
	}
	return nil
}

func baselinePlanInjectionSpec(spec agentlaunch.InjectionSpec, opts baselineWriteOptions) (baselineDryRunPlan, error) {
	files, err := baselinePlanInjectionFiles(spec, opts)
	if err != nil {
		return baselineDryRunPlan{}, err
	}
	plan := baselineDryRunPlan{
		Files:       make([]baselinePlannedFile, 0, len(files)),
		NativeFiles: append([]agentlaunch.NativeFile(nil), spec.NativeFiles...),
	}
	for _, f := range files {
		plan.Files = append(plan.Files, baselinePlannedFile{RelPath: f.file.RelPath, Mode: f.file.Mode, Source: f.source})
		if f.source == baselineSourceOverlay {
			plan.Overlays = append(plan.Overlays, f.file)
		}
	}
	return plan, nil
}

func (w baselineWriter) WriteInjectionSpec(bootDir string, spec agentlaunch.InjectionSpec, opts baselineWriteOptions) (baselineWriteResult, error) {
	if strings.TrimSpace(bootDir) == "" {
		return baselineWriteResult{}, baselineErrEmptyBootDir
	}
	files, err := baselinePlanInjectionFiles(spec, opts)
	if err != nil {
		return baselineWriteResult{}, err
	}
	return w.writePlannedFiles(bootDir, files)
}

func (w baselineWriter) WriteFiles(bootDir string, files []baselineFile) (baselineWriteResult, error) {
	if strings.TrimSpace(bootDir) == "" {
		return baselineWriteResult{}, baselineErrEmptyBootDir
	}
	planned := make([]baselineInternalPlannedFile, 0, len(files))
	for _, file := range files {
		normalized, err := baselineNormalizeFile(file)
		if err != nil {
			return baselineWriteResult{}, err
		}
		planned = append(planned, baselineInternalPlannedFile{file: normalized, source: baselineSourceFile})
	}
	return w.writePlannedFiles(bootDir, planned)
}

func (w baselineWriter) writePlannedFiles(bootDir string, files []baselineInternalPlannedFile) (baselineWriteResult, error) {
	entries := make([]artifact.Entry, 0, len(files))
	result := baselineWriteResult{Files: make([]baselineWrittenFile, 0, len(files))}
	for _, planned := range files {
		entries = baselineUpsertBootdirEntry(entries, artifact.Entry{
			Path:  planned.file.RelPath,
			Kind:  artifact.EntryFile,
			Mode:  planned.file.Mode,
			Bytes: []byte(planned.file.Content),
			Ownership: artifact.Ownership{
				EntryID: "agentruntime.bootdir:" + planned.file.RelPath,
				GroupID: "agentruntime.bootdir:" + planned.source,
			},
			Provenance: artifact.Provenance{Source: "agentruntime/bootdir", Note: planned.source},
		})
		result.Files = append(result.Files, baselineWrittenFile{RelPath: planned.file.RelPath, Mode: planned.file.Mode, Source: planned.source})
	}

	normalized, err := artifact.Normalize(entries)
	if err != nil {
		return baselineWriteResult{}, err
	}
	if _, err := agentlaunch.MaterializeArtifacts(context.Background(), agentlaunch.ArtifactMaterializationRequest{
		TargetRoot: bootDir,
		Roots:      agentlaunch.ExecutionRoots{BootRoot: bootDir},
		Artifacts:  artifact.Tree{Entries: normalized},
		Authorize:  w.Authorize,
		Operation:  materialize.OperationReconcile,
		Reconcile:  materialize.ReconcilePolicy{Conflict: materialize.ConflictReport},
	}); err != nil {
		return baselineWriteResult{}, fmt.Errorf("bootdir: materialize: %w", err)
	}
	if w.OnWritten != nil {
		for _, file := range result.Files {
			w.OnWritten(file)
		}
	}
	return result, nil
}

func baselineUpsertBootdirEntry(entries []artifact.Entry, next artifact.Entry) []artifact.Entry {
	for i := range entries {
		if entries[i].Path == next.Path {
			entries[i] = next
			return entries
		}
	}
	return append(entries, next)
}

type baselineInternalPlannedFile struct {
	file   baselineFile
	source string
}

func baselinePlanInjectionFiles(spec agentlaunch.InjectionSpec, opts baselineWriteOptions) ([]baselineInternalPlannedFile, error) {
	files := make([]baselineInternalPlannedFile, 0, len(spec.NativeFiles)+len(spec.BootDirOverlay))
	for _, nf := range spec.NativeFiles {
		file, err := baselineResolveNativeFile(nf, opts.baselineNativeResolver)
		if err != nil {
			return nil, err
		}
		files = append(files, baselineInternalPlannedFile{file: file, source: baselineSourceNative})
	}
	keys := make([]string, 0, len(spec.BootDirOverlay))
	for key := range spec.BootDirOverlay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		file, err := baselineNormalizeFile(baselineFile{RelPath: key, Content: spec.BootDirOverlay[key]})
		if err != nil {
			return nil, err
		}
		files = append(files, baselineInternalPlannedFile{file: file, source: baselineSourceOverlay})
	}
	return files, nil
}

func baselineResolveNativeFile(nf agentlaunch.NativeFile, resolver baselineNativeResolver) (baselineFile, error) {
	if nf.Kind != agentlaunch.NativeFileRaw {
		if resolver == nil {
			return baselineFile{}, fmt.Errorf("%w: %q", baselineErrUnsupportedNativeKind, nf.Kind)
		}
		file, err := resolver(nf)
		if err != nil {
			return baselineFile{}, err
		}
		return baselineNormalizeFile(file)
	}
	return baselineNormalizeFile(baselineFile{RelPath: nf.RelPath, Content: nf.Content, Mode: nf.Mode})
}

func baselineNormalizeFile(file baselineFile) (baselineFile, error) {
	if file.RelPath == "" {
		return baselineFile{}, baselineErrEmptyRelPath
	}
	if err := baselineValidateRelPath(file.RelPath); err != nil {
		return baselineFile{}, err
	}
	if file.Mode == 0 {
		file.Mode = 0o644
	}
	return file, nil
}

// baselineTaskBundle returns baselineRaw native files under root. The caller owns body
// rendering; the helper pins Torque/Tether's safe task bundle planting shape.
func baselineTaskBundle(root string, files map[string]string) ([]agentlaunch.NativeFile, error) {
	if root == "" {
		root = "tasks"
	}
	if err := baselineValidateRelPath(root + "/README.md"); err != nil {
		return nil, err
	}
	out := make([]agentlaunch.NativeFile, 0, len(files))
	for rel, content := range files {
		path := root + "/" + rel
		nf := agentlaunch.NativeFile{Kind: agentlaunch.NativeFileRaw, RelPath: path, Content: content}
		if err := nf.Validate(); err != nil {
			return nil, err
		}
		out = append(out, nf)
	}
	return out, nil
}
