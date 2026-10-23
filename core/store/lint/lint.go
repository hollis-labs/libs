// Package lint checks SQL strings against the portable-SQL rules, so a
// statement that would only run on SQLite (or only on Postgres) fails in a unit
// test instead of in production.
//
// Check works on one statement. [File] and [Dir] find the SQL string literals
// in Go source and also flag calls to LastInsertId. A finding can be waived with
// a comment on the same or the previous line:
//
//	// gostore:allow insert-or   (or "gostore:allow" for every rule)
//
// Migration files are per dialect by design and are not checked.
package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/libs/core/store"
)

// Finding is one rule violation.
type Finding struct {
	Rule    string
	Message string
	// File and Line locate a Go finding; they are zero for [Check].
	File string
	Line int
	// SQL is the offending statement (trimmed to 80 bytes), for context.
	SQL string
}

func (f Finding) String() string {
	loc := ""
	if f.File != "" {
		loc = fmt.Sprintf("%s:%d: ", f.File, f.Line)
	}
	return fmt.Sprintf("%s%s: %s [%s]", loc, f.Rule, f.Message, f.SQL)
}

type rule struct {
	name string
	re   *regexp.Regexp
	msg  string
}

func r(name, pattern, msg string) rule {
	return rule{name, regexp.MustCompile(`(?i)` + pattern), msg}
}

var rules = []rule{
	r("insert-or", `\bINSERT\s+OR\s+(REPLACE|IGNORE|ABORT|FAIL|ROLLBACK)\b|\bREPLACE\s+INTO\b`, "use INSERT ... ON CONFLICT (...) DO NOTHING / DO UPDATE"),
	r("sqlite-time", `\b(datetime|strftime|julianday|unixepoch)\s*\(|\bdate\s*\(\s*\)`, "time comes from Go as a parameter"),
	r("db-clock", `\bnow\s*\(|\bCURRENT_(TIMESTAMP|DATE|TIME)\b|\bLOCALTIMESTAMP\b`, "time comes from Go as a parameter; the database clock is not portable"),
	r("autoincrement", `\bAUTOINCREMENT\b|\b(BIG|SMALL)?SERIAL\b`, "no AUTOINCREMENT/serial in shared SQL; use a Go-generated id or RETURNING"),
	r("ilike", `\bILIKE\b`, "use lower(col) LIKE lower(?) (store.LikeInsensitive)"),
	r("json-path", `\bjson_extract\s*\(|->>|#>>|\bjsonb_\w+\s*\(`, "use the Dialect JSON helpers (JSONText, JSONInt, ...)"),
	r("dollar-placeholder", `\$\d+`, "use ? placeholders; the store rebinds them"),
	r("sqlite-only", `\bPRAGMA\b|\bGLOB\b|\bIFNULL\s*\(|\bINSERT\s+INTO\s+\w+\s+DEFAULT\s+VALUES\b`, "SQLite-only construct (use COALESCE, or keep it out of shared SQL)"),
}

var likeRe = regexp.MustCompile(`(?i)\bLIKE\b`)
var lowerLikeRe = regexp.MustCompile(`(?i)lower\s*\([^()]*\)\s+(NOT\s+)?LIKE\s+lower\s*\(`)

// Check returns the rule violations in one SQL statement. String literals,
// quoted identifiers and comments are ignored.
func Check(query string) []Finding {
	m := store.Mask(query)
	var out []Finding
	for _, rl := range rules {
		if rl.re.MatchString(m) {
			out = append(out, Finding{Rule: rl.name, Message: rl.msg, SQL: short(query)})
		}
	}
	for _, loc := range likeRe.FindAllStringIndex(m, -1) {
		start := loc[0] - 60
		if start < 0 {
			start = 0
		}
		end := loc[1] + 20
		if end > len(m) {
			end = len(m)
		}
		if !lowerLikeRe.MatchString(m[start:end]) {
			out = append(out, Finding{Rule: "like-case", Message: "LIKE is case-insensitive on SQLite and case-sensitive on Postgres; write lower(col) LIKE lower(?)", SQL: short(query)})
			break
		}
	}
	return out
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

var sqlStart = regexp.MustCompile(`(?is)^\s*(SELECT|INSERT|UPDATE|DELETE|WITH|CREATE|ALTER|DROP|REPLACE)\s+\S`)

// File lints the SQL string literals and LastInsertId calls in one Go source
// file.
func File(filename string, src []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	waived := map[int][]string{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if i := strings.Index(c.Text, "gostore:allow"); i >= 0 {
				line := fset.Position(c.Pos()).Line
				waived[line] = append(waived[line], strings.Fields(c.Text[i+len("gostore:allow"):])...)
			}
		}
	}
	isWaived := func(rule string, line int) bool {
		for _, l := range []int{line, line - 1} {
			ws, ok := waived[l]
			if !ok {
				continue
			}
			if len(ws) == 0 {
				return true
			}
			for _, w := range ws {
				if w == rule {
					return true
				}
			}
		}
		return false
	}
	var out []Finding
	add := func(fd Finding, pos token.Pos) {
		fd.File = filename
		fd.Line = fset.Position(pos).Line
		if !isWaived(fd.Rule, fd.Line) {
			out = append(out, fd)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(x.Value)
			if err != nil || !sqlStart.MatchString(s) {
				return true
			}
			for _, fd := range Check(s) {
				add(fd, x.Pos())
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "LastInsertId" {
				add(Finding{Rule: "last-insert-id", Message: "LastInsertId is not portable; use INSERT ... RETURNING id", SQL: "LastInsertId()"}, x.Pos())
			}
		}
		return true
	})
	return out, nil
}

// Options controls [Dir].
type Options struct {
	// SkipTests leaves *_test.go files out.
	SkipTests bool
}

// Dir lints every Go file under root, skipping vendor, testdata and hidden
// directories. Findings come back sorted by file and line.
func Dir(root string, opts Options) ([]Finding, error) {
	var out []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || opts.SkipTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fs, err := File(path, src)
		if err != nil {
			return fmt.Errorf("lint: %s: %w", path, err)
		}
		out = append(out, fs...)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, err
}
