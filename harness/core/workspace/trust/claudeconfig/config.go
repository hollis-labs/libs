// Package claudeconfig updates explicitly supplied Claude configuration roots.
// Confinement relies on cooperating mutators holding the declared root locks;
// it cannot protect against arbitrary same-user renames or entry swaps.
package claudeconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace/trust"
)

const configName = ".claude.json"
const lockName = configName + ".lock"
const maxConfig = 4 << 20
const maxJSONDepth = 10000

var errConfig = errors.New("trust configuration refused")
var errChanged = errors.New("trust resource changed")

type Port struct {
	platform   string
	openConfig func(*os.Root, string) (*os.File, error)
}

func New() *Port { return &Port{platform: runtime.GOOS} }
func (p *Port) Supported(m trust.Mechanism) bool {
	return p != nil && (p.platform == "linux" || p.platform == "darwin") && m == trust.ClaudeProjects
}

type session struct {
	root               *os.Root
	lockFile           *os.File
	path               string
	rootInfo, lockInfo fs.FileInfo
	closed             bool
	// Per-session durability seam; production defaults to directory fsync.
	syncDirectory func(*os.Root) error
	openConfig    func(*os.Root, string) (*os.File, error)
}

func target(r trust.Request) (string, error) {
	p, e := filepath.EvalSymlinks(filepath.Dir(r.Target.LogicalPath))
	if e != nil || p != r.Target.CanonicalParent {
		return "", errConfig
	}
	st, e := os.Lstat(p)
	if e != nil || !st.IsDir() {
		return "", errConfig
	}
	want := filepath.Join(p, filepath.Base(r.Target.LogicalPath))
	st, e = os.Lstat(r.Target.LogicalPath)
	if e == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", errConfig
		}
		actual, e := filepath.EvalSymlinks(r.Target.LogicalPath)
		if e != nil || actual != want {
			return "", errConfig
		}
	} else if !os.IsNotExist(e) {
		return "", errConfig
	}
	return want, nil
}
func open(r trust.Request) (*session, error) {
	canonical, e := filepath.EvalSymlinks(r.Config.Path)
	if e != nil || canonical != r.Config.Path {
		return nil, errConfig
	}
	st, e := os.Lstat(r.Config.Path)
	if e != nil || !safeDirectory(st) {
		return nil, errConfig
	}
	root, e := os.OpenRoot(r.Config.Path)
	if e != nil {
		return nil, errConfig
	}
	actual, e := root.Stat(".")
	if e != nil || !os.SameFile(st, actual) {
		root.Close()
		return nil, errConfig
	}
	return &session{root: root, path: r.Config.Path, rootInfo: st, syncDirectory: syncDirectory}, nil
}

func syncDirectory(root *os.Root) error {
	dir, e := root.Open(".")
	if e != nil {
		return errConfig
	}
	e = dir.Sync()
	ce := dir.Close()
	if e != nil || ce != nil {
		return errConfig
	}
	return nil
}
func (s *session) validate() error {
	st, e := os.Lstat(s.path)
	if e != nil || !safeDirectory(st) || !os.SameFile(st, s.rootInfo) {
		return errChanged
	}
	if s.lockInfo != nil {
		st, e = s.root.Lstat(lockName)
		if e != nil || !st.Mode().IsRegular() || !os.SameFile(st, s.lockInfo) {
			return errChanged
		}
	}
	return nil
}

type snapshot struct {
	bytes []byte
	info  fs.FileInfo
}

func (s *session) read() (snapshot, error) {
	if e := s.validate(); e != nil {
		return snapshot{}, e
	}
	st, e := s.root.Lstat(configName)
	if os.IsNotExist(e) {
		return snapshot{}, nil
	}
	if e != nil || !safeFile(st) || st.Size() > maxConfig {
		return snapshot{}, errConfig
	}
	probe := s.openConfig
	if probe == nil {
		probe = openConfigReadOnly
	}
	f, e := probe(s.root, configName)
	if e != nil {
		return snapshot{}, errConfig
	}
	actual, e := f.Stat()
	if e != nil || !safeFile(actual) || actual.Size() > maxConfig || !os.SameFile(st, actual) {
		f.Close()
		return snapshot{}, errChanged
	}
	b, e := io.ReadAll(io.LimitReader(f, maxConfig+1))
	ce := f.Close()
	if e != nil || ce != nil || len(b) > maxConfig {
		return snapshot{}, errConfig
	}
	again, e := s.root.Lstat(configName)
	if e != nil || !os.SameFile(st, again) {
		return snapshot{}, errChanged
	}
	return snapshot{bytes: b, info: st}, nil
}

// Reject unpaired surrogate escapes before decoding can normalize unrelated
// keys. Valid pairs and literal escaped backslashes retain their meaning.
func validUnicodeEscapes(raw []byte) bool {
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return !quoted
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return nil, errConfig
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if uniqueValue(decoder) != nil {
		return nil, errConfig
	}
	if _, e := decoder.Token(); e != io.EOF {
		return nil, errConfig
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errConfig
	}
	return obj, nil
}

// Reject duplicate object keys rather than silently discard unrelated values.
func uniqueValue(d *json.Decoder) error { return walkJSON(d, 0) }

func walkJSON(d *json.Decoder, depth int) error {
	token, e := d.Token()
	if e != nil {
		return errConfig
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxJSONDepth {
		return errConfig
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return errConfig
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errConfig
			}
			seen[key] = true
			if walkJSON(d, depth+1) != nil {
				return errConfig
			}
		}
	case '[':
		for d.More() {
			if walkJSON(d, depth+1) != nil {
				return errConfig
			}
		}
	default:
		return errConfig
	}
	_, e = d.Token()
	if e != nil {
		return errConfig
	}
	return nil
}
func configuration(b []byte, target string) (map[string]json.RawMessage, map[string]json.RawMessage, map[string]json.RawMessage, bool, error) {
	var obj map[string]json.RawMessage
	var e error
	if b == nil {
		obj = map[string]json.RawMessage{}
	} else {
		obj, e = object(b)
		if e != nil {
			return nil, nil, nil, false, e
		}
	}
	projects := map[string]json.RawMessage{}
	if raw, ok := obj["projects"]; ok {
		projects, e = object(raw)
		if e != nil {
			return nil, nil, nil, false, e
		}
	}
	entry := map[string]json.RawMessage{}
	if raw, ok := projects[target]; ok {
		entry, e = object(raw)
		if e != nil {
			return nil, nil, nil, false, e
		}
	}
	present := true
	for _, key := range []string{"hasTrustDialogAccepted", "hasCompletedProjectOnboarding"} {
		raw, ok := entry[key]
		if !ok {
			present = false
			continue
		}
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return nil, nil, nil, false, errConfig
		}
		present = present && value
	}
	return obj, projects, entry, present, nil
}
func (p *Port) Observe(ctx context.Context, r trust.Request) (trust.Observation, error) {
	if !p.Supported(r.Mechanism) || ctx.Err() != nil {
		return trust.Observation{}, errConfig
	}
	t, e := target(r)
	if e != nil {
		return trust.Observation{}, e
	}
	s, e := open(r)
	if e != nil {
		return trust.Observation{}, e
	}
	defer s.root.Close()
	s.openConfig = p.openConfig
	snap, e := s.read()
	if e != nil {
		return trust.Observation{}, e
	}
	_, _, _, present, e := configuration(snap.bytes, t)
	return trust.Observation{Target: t, Present: present}, e
}
func (p *Port) Begin(ctx context.Context, r trust.Request) (trust.Session, error) {
	if !p.Supported(r.Mechanism) || ctx.Err() != nil {
		return nil, errConfig
	}
	s, e := open(r)
	if e != nil {
		return nil, e
	}
	s.openConfig = p.openConfig
	f, e := s.root.OpenFile(lockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		s.root.Close()
		return nil, errConfig
	}
	s.lockInfo, e = f.Stat()
	s.lockFile = f
	if e != nil || s.lockInfo == nil {
		return s, errChanged
	}
	return s, nil
}
func (s *session) Apply(ctx context.Context, r trust.Request, validate func(context.Context) error) (out trust.Observation, mutated bool, retErr error) {
	if s.closed || s.lockInfo == nil || r.Config.Path != s.path || validate == nil {
		return trust.Observation{}, false, errConfig
	}
	t, e := target(r)
	if e != nil {
		return trust.Observation{}, false, e
	}
	snap, e := s.read()
	if e != nil {
		return trust.Observation{}, false, e
	}
	obj, projects, entry, present, e := configuration(snap.bytes, t)
	observed := trust.Observation{Target: t, Present: present}
	if e != nil {
		return observed, false, e
	}
	if ctx.Err() != nil || validate(ctx) != nil {
		return observed, false, errChanged
	}
	if present {
		return observed, false, nil
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	entry["hasCompletedProjectOnboarding"] = json.RawMessage("true")
	raw, e := json.Marshal(entry)
	if e != nil {
		return observed, false, errConfig
	}
	projects[t] = raw
	raw, e = json.Marshal(projects)
	if e != nil {
		return observed, false, errConfig
	}
	obj["projects"] = raw
	body, e := json.MarshalIndent(obj, "", "  ")
	if e != nil {
		return observed, false, errConfig
	}
	body = append(body, '\n')
	if len(body) > maxConfig {
		return observed, false, errConfig
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return observed, false, errConfig
	}
	temp := ".claude-trust-" + hex.EncodeToString(nonce) + ".tmp"
	f, e := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return observed, false, errConfig
	}
	// Keep the inode pinned until replacement or conditional cleanup finishes.
	// A filename alone does not prove custody of a temporary entry.
	tempInfo, statErr := f.Stat()
	renamed := false
	checkTemp := func() error {
		if statErr != nil || !safeFile(tempInfo) || s.validate() != nil {
			return errChanged
		}
		current, err := s.root.Lstat(temp)
		if err != nil || !safeFile(current) || !os.SameFile(tempInfo, current) {
			return errChanged
		}
		return nil
	}
	defer func() {
		if !renamed {
			if checkTemp() != nil {
				mutated = true
				retErr = errChanged
			} else if e := s.root.Remove(temp); e != nil {
				mutated = true
				retErr = errChanged
			}
		}
		if f.Close() != nil {
			mutated = true
			retErr = errConfig
		}
	}()
	if statErr != nil || !safeFile(tempInfo) {
		return observed, true, errChanged
	}
	n, e := f.Write(body)
	if e == nil && n != len(body) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = f.Sync()
	}
	if e != nil {
		return observed, false, errConfig
	}
	// Recheck the complete config privately; neither its bytes nor a content hash
	// is exposed in diagnostics or receipts. Providers ignoring locks remain
	// outside the cooperating-mutator guarantee.
	if ctx.Err() != nil || validate(ctx) != nil {
		return observed, false, errChanged
	}
	current, e := s.read()
	if e != nil || !sameSnapshot(snap, current) {
		return observed, false, errChanged
	}
	again, e := target(r)
	if e != nil || again != t {
		return observed, false, errChanged
	}
	if e = s.validate(); e != nil {
		return observed, false, e
	}
	if checkTemp() != nil {
		return observed, true, errChanged
	}
	if e = s.root.Rename(temp, configName); e != nil {
		return observed, true, errConfig
	}
	renamed = true
	// Any failure after rename means the effect may already be visible.
	observed.Present = true
	if s.syncDirectory(s.root) != nil {
		return observed, true, errConfig
	}
	final, e := s.read()
	if e != nil {
		return observed, true, e
	}
	_, _, _, observed.Present, e = configuration(final.bytes, t)
	if e != nil || !observed.Present {
		return observed, true, errChanged
	}
	return observed, true, nil
}
func sameSnapshot(a, b snapshot) bool {
	if (a.info == nil) != (b.info == nil) {
		return false
	}
	return (a.info == nil || os.SameFile(a.info, b.info)) && bytes.Equal(a.bytes, b.bytes)
}
func (s *session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	e := s.validate()
	if e == nil && s.lockInfo != nil {
		e = s.root.Remove(lockName)
		if e == nil {
			e = s.syncDirectory(s.root)
		}
	} else if s.lockInfo == nil {
		e = errChanged
	}
	var lockErr error
	if s.lockFile != nil {
		lockErr = s.lockFile.Close()
	}
	ce := s.root.Close()
	if e != nil || ce != nil || lockErr != nil {
		return errChanged
	}
	return nil
}
