package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/agent-contracts-leaf/capabilities"
	agentdef "github.com/hollis-labs/go-agentdef"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usageText = `usage: agentdef <command> [flags] <path>...

commands:
  validate   parse, validate fields, resolve skill references
  lint       validate, then non-fatal findings (--strict: findings fail too)
  digest     print "<path>\tsha256:<hex>" per definition (--skills: pinned skill hashes too)
  check      recompute agentdef:generated span hashes; fail on drift

flags:
  --layer root=<dir>,precedence=<int>   repeatable; load a directory as one layer
`

type layerSpec struct {
	root       string
	precedence int
}

type layerFlags []layerSpec

func (l *layerFlags) String() string { return fmt.Sprint(*l) }

func (l *layerFlags) Set(v string) error {
	var spec layerSpec
	seenRoot := false
	for _, part := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			return fmt.Errorf("want root=<path>,precedence=<int>, got %q", v)
		}
		switch k {
		case "root":
			spec.root, seenRoot = val, val != ""
		case "precedence":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("precedence: %w", err)
			}
			spec.precedence = n
		default:
			return fmt.Errorf("unknown layer key %q", k)
		}
	}
	if !seenRoot {
		return errors.New("layer needs root=<path>")
	}
	*l = append(*l, spec)
	return nil
}

// target is one definition to act on, with where its layer lives.
type target struct {
	display string // path shown to the user
	def     *agentdef.Definition
	fsys    fs.FS
	root    string // layer root within fsys
	rootKey string // identifies the layer root for orphan detection
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd := args[0]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "validate", "lint", "digest", "check":
	default:
		fmt.Fprintf(stderr, "agentdef: unknown command %q\n%s", cmd, usageText)
		return 2
	}

	fset := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fset.SetOutput(stderr)
	var layers layerFlags
	fset.Var(&layers, "layer", "root=<dir>,precedence=<int> (repeatable)")
	var strict, withSkills bool
	switch cmd {
	case "lint":
		fset.BoolVar(&strict, "strict", false, "exit non-zero on findings too")
	case "digest":
		fset.BoolVar(&withSkills, "skills", false, "also print each resolved skill's pinned hash")
	}

	// Flags and paths may be interspersed.
	var paths []string
	rest := args[1:]
	for {
		if err := fset.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest = fset.Args()
		if len(rest) == 0 {
			break
		}
		paths = append(paths, rest[0])
		rest = rest[1:]
	}
	if len(paths) == 0 && len(layers) == 0 {
		fmt.Fprintf(stderr, "agentdef %s: need at least one path or --layer\n", cmd)
		return 2
	}

	targets, failed := load(paths, layers, stderr)
	code := 0
	if failed {
		code = 1
	}

	switch cmd {
	case "validate":
		for _, t := range targets {
			if !validateTarget(t, stderr) {
				code = 1
			}
		}
	case "lint":
		findings := 0
		for _, t := range targets {
			if !validateTarget(t, stderr) {
				code = 1
			}
		}
		for _, t := range targets {
			for _, f := range agentdef.Lint(t.def) {
				findings++
				fmt.Fprintf(stdout, "%s: %s: %s: %s\n", t.display, f.Rule, f.Severity, f.Message)
			}
		}
		for _, o := range orphans(targets) {
			findings++
			fmt.Fprintf(stdout, "%s: orphan-skill: warn: nothing references this skill\n", o)
		}
		if strict && findings > 0 {
			code = 1
		}
	case "digest":
		for _, t := range targets {
			if !validateTarget(t, stderr) {
				code = 1
				continue
			}
			dg, err := agentdef.Digest(t.def)
			if err != nil {
				fmt.Fprintf(stderr, "%s: digest: %v\n", t.display, err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s\t%s\n", t.display, dg)
			if withSkills {
				refs, err := agentdef.ResolveSkills(t.fsys, t.root, t.def)
				if err != nil {
					fmt.Fprintf(stderr, "%s: skills: %v\n", t.display, err)
					code = 1
					continue
				}
				for _, r := range refs {
					fmt.Fprintf(stdout, "%s\t%s\t%s\n", t.display, r.Path, r.Hash)
				}
			}
		}
	case "check":
		for _, t := range targets {
			stale, err := agentdef.CheckGenerated(t.fsys, t.root, t.def)
			if err != nil {
				fmt.Fprintf(stderr, "%s: generated: %v\n", t.display, err)
				code = 1
				continue
			}
			for _, s := range stale {
				fmt.Fprintf(stderr, "%s: generated: stale span source=%s pinned=%s computed=%s\n",
					t.display, s.Span.Source, s.Span.PinnedHash, s.ComputedHash)
				code = 1
			}
		}
	}
	return code
}

// load parses every named file and every layer. A file that fails to parse is
// reported and skipped; failed says whether anything was reported.
func load(paths []string, layers layerFlags, stderr io.Writer) (targets []target, failed bool) {
	for _, p := range paths {
		dir, base := filepath.Split(p)
		if dir == "" {
			dir = "."
		}
		fsys := os.DirFS(dir)
		d, err := agentdef.ParseFile(fsys, base)
		if err != nil {
			reportErr(stderr, p, err)
			failed = true
			continue
		}
		targets = append(targets, target{display: p, def: d, fsys: fsys, root: ".", rootKey: filepath.Clean(dir)})
	}

	if len(layers) == 0 {
		return targets, failed
	}
	var ls []agentdef.Layer
	roots := map[string]string{}
	for _, l := range layers {
		ls = append(ls, agentdef.Layer{FS: os.DirFS(l.root), Root: ".", Name: l.root, Precedence: l.precedence})
		roots[l.root] = l.root
	}
	set, err := agentdef.LoadLayers(ls)
	if err != nil {
		var ce *agentdef.CollisionError
		if errors.As(err, &ce) {
			for _, e := range splitJoined(err) {
				fmt.Fprintf(stderr, "%s: collision: %v\n", strings.Join(collisionLayers(e), ","), e)
			}
		} else {
			fmt.Fprintf(stderr, "layer: load: %v\n", err)
		}
		failed = true
	}
	if set == nil {
		return targets, failed
	}
	names := make([]string, 0, len(set.ByName))
	for n := range set.ByName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := set.ByName[n]
		root := roots[d.Layer]
		targets = append(targets, target{
			display: filepath.Join(root, filepath.FromSlash(d.SourceRef)),
			def:     d, fsys: os.DirFS(root), root: ".", rootKey: filepath.Clean(root),
		})
	}
	return targets, failed
}

func splitJoined(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok { //nolint:errorlint // errors.Join exposes its members only through this interface
		return j.Unwrap()
	}
	return []error{err}
}

func collisionLayers(err error) []string {
	var ce *agentdef.CollisionError
	if errors.As(err, &ce) {
		return ce.Layers
	}
	return []string{"layer"}
}

// knownCapability backs requires/uses validation with the shared capability
// vocabulary. The library itself takes any func; only this CLI binds it.
func knownCapability(name string) bool {
	return capabilities.Known(capabilities.Name(name))
}

// validateTarget runs Validate and skill resolution; it reports each problem
// as "<path>: <field>: <message>" and returns whether the target is clean.
func validateTarget(t target, stderr io.Writer) bool {
	ok := true
	if err := t.def.Validate(agentdef.WithCapabilities(knownCapability)); err != nil {
		reportErr(stderr, t.display, err)
		ok = false
	}
	if _, err := agentdef.ResolveSkills(t.fsys, t.root, t.def); err != nil {
		fmt.Fprintf(stderr, "%s: skills: %v\n", t.display, err)
		ok = false
	}
	return ok
}

func reportErr(stderr io.Writer, display string, err error) {
	var ve *agentdef.ValidationError
	if errors.As(err, &ve) {
		for _, fe := range ve.Errors {
			fmt.Fprintf(stderr, "%s: %s: %s\n", display, fe.Field, fe.Message)
		}
		return
	}
	fmt.Fprintf(stderr, "%s: frontmatter: %v\n", display, err)
}

// orphans lists skills/<name>/ directories beside the targets that no target
// in the same root references.
func orphans(targets []target) []string {
	type root struct {
		fsys fs.FS
		dir  string
		key  string
	}
	seen := map[string]root{}
	used := map[string]bool{}
	for _, t := range targets {
		seen[t.rootKey] = root{t.fsys, t.root, t.rootKey}
		for _, s := range t.def.Skills {
			used[t.rootKey+"\x00"+s] = true
		}
	}
	var out []string
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := seen[k]
		entries, err := fs.ReadDir(r.fsys, path.Join(r.dir, "skills"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && !used[k+"\x00"+e.Name()] {
				out = append(out, filepath.Join(r.key, "skills", e.Name()))
			}
		}
	}
	return out
}
