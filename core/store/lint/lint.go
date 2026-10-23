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

	"github.com/hollis-labs/libs/core/store/internal/sqlmask"
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
	// when, if set, restricts the rule to statements it matches.
	when *regexp.Regexp
}

func r(name, pattern, msg string) rule {
	return rule{name: name, re: regexp.MustCompile(`(?i)` + pattern), msg: msg}
}

// unknown stands for a non-literal operand folded out of a concatenation.
const unknown = "\x01"

var ddlStart = regexp.MustCompile(`(?i)^\s*(CREATE|ALTER)\b`)

var rules = []rule{
	r("insert-or", `\bINSERT\s+OR\s+(REPLACE|IGNORE|ABORT|FAIL|ROLLBACK)\b|\bREPLACE\s+INTO\b`, "use INSERT ... ON CONFLICT (...) DO NOTHING / DO UPDATE"),
	r("sqlite-time", `\b(datetime|strftime|julianday|unixepoch)\s*\(|\bdate\s*\(\s*\)`, "time comes from Go as a parameter"),
	r("db-clock", `\bnow\s*\(|\bCURRENT_(TIMESTAMP|DATE|TIME)\b|\bLOCALTIMESTAMP\b`, "time comes from Go as a parameter; the database clock is not portable"),
	r("autoincrement", `\bAUTOINCREMENT\b`, "no AUTOINCREMENT in shared SQL; use a Go-generated id or RETURNING"),
	{name: "autoincrement", re: regexp.MustCompile(`(?i)\b(BIG|SMALL)?SERIAL\b`), msg: "no serial column in shared SQL; use a Go-generated id or RETURNING", when: ddlStart},
	r("ilike", `\bILIKE\b`, "use lower(col) LIKE lower(?) (store.LikeInsensitive)"),
	r("json-path", `\bjson_extract\s*\(|->>|#>>|\bjsonb_\w+\s*\(`, "use the Dialect JSON helpers (JSONText, JSONInt, ...)"),
	r("dollar-placeholder", `\$\d+`, "use ? placeholders; the store rebinds them"),
	r("sqlite-only", `\bPRAGMA\b|\bGLOB\b|\bIFNULL\s*\(|\bgroup_concat\s*\(|\bINSERT\s+INTO\s+\w+\s+DEFAULT\s+VALUES\b`, "SQLite-only construct (use COALESCE, or keep it out of shared SQL)"),
	r("limit-comma", `\bLIMIT\s+(\d+|\?)\s*,\s*(\d+|\?)`, "write LIMIT n OFFSET m; LIMIT a, b is SQLite-only"),
	r("jsonb-operator", `(^|[^?])\?[|&]`, "?| and ?& are Postgres JSONB operators and collide with the placeholder; use the Dialect JSON helpers"),
}

var likeRe = regexp.MustCompile(`(?i)\bLIKE\b`)
var lowerLikeRe = regexp.MustCompile(`(?i)(?:gostore_)?lower\s*\((?:[^()]|\([^()]*\))*\)\s+(NOT\s+)?LIKE\s+(?:gostore_)?lower\s*\(`)

// Check returns the rule violations in one SQL statement. String literals,
// quoted identifiers and comments are ignored.
func Check(query string) []Finding {
	m := sqlmask.Mask(query)
	var out []Finding
	for _, rl := range rules {
		if rl.when != nil && !rl.when.MatchString(m) {
			continue
		}
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
		before := strings.TrimSpace(m[:loc[0]])
		after := strings.TrimSpace(m[loc[1]:])
		if strings.HasSuffix(before, unknown) || strings.HasPrefix(after, unknown) {
			continue // an operand built at run time: cannot tell
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
	env := constStrings(f)
	sqlVars := map[string]bool{}
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
		case *ast.BinaryExpr:
			// SQL built as "SELECT ..." + "FROM ...": fold the chain so a rule
			// that spans the pieces still sees one statement. Same-file string
			// constants are resolved; any other operand folds to a placeholder.
			if x.Op != token.ADD {
				return true
			}
			s, lits, viaVar := foldString(x, env, sqlVars)
			if lits == 0 || !(sqlStart.MatchString(s) || viaVar) {
				return true
			}
			for _, fd := range Check(s) {
				add(fd, x.Pos())
			}
			return false
		case *ast.AssignStmt:
			// q := "SELECT ..."; q += " WHERE x ILIKE ?": a variable that starts as
			// SQL has its appended pieces checked as they come.
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return true
			}
			id, ok := x.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			s, lits, _ := foldString(x.Rhs[0], env, sqlVars)
			switch {
			case x.Tok == token.ADD_ASSIGN && sqlVars[id.Name] && lits > 0:
				for _, fd := range Check(s) {
					add(fd, x.Pos())
				}
				return false
			case (x.Tok == token.DEFINE || x.Tok == token.ASSIGN) && lits > 0 && sqlStart.MatchString(s):
				sqlVars[id.Name] = true
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

// foldString joins a chain of "+" operands into one string. Same-file string
// constants are resolved; other operands become [unknown]. It returns the number
// of literal operands folded and whether the chain starts from a variable that
// holds SQL.
func foldString(e ast.Expr, env map[string]string, sqlVars map[string]bool) (string, int, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if s, err := strconv.Unquote(x.Value); err == nil {
				return s, 1, false
			}
		}
	case *ast.Ident:
		if v, ok := env[x.Name]; ok {
			return v, 1, false
		}
		if sqlVars[x.Name] {
			return unknown, 0, true
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			l, ln, lv := foldString(x.X, env, sqlVars)
			r, rn, rv := foldString(x.Y, env, sqlVars)
			return l + r, ln + rn, lv || rv
		}
	case *ast.ParenExpr:
		return foldString(x.X, env, sqlVars)
	}
	return unknown, 0, false
}

// constStrings collects the string constants (and package-level string
// variables with a literal value) declared in a file, resolving ones built from
// earlier ones.
func constStrings(f *ast.File) map[string]string {
	env := map[string]string{}
	for pass := 0; pass < 3; pass++ { // a const may refer to a later one
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				return true
			}
			for i, name := range vs.Names {
				if s, lits, _ := foldString(vs.Values[i], env, nil); lits > 0 && !strings.Contains(s, unknown) {
					env[name.Name] = s
				}
			}
			return true
		})
	}
	return env
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
