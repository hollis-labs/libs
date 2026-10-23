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
	"slices"
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
	r("sqlite-only", `\bPRAGMA\b|\bGLOB\b|\bIFNULL\s*\(|\bgroup_concat\s*\(|\bINSERT\s+INTO\s+[\w\x01]+\s+DEFAULT\s+VALUES\b`, "SQLite-only construct (use COALESCE, or keep it out of shared SQL)"),
	r("limit-comma", `\bLIMIT\s+(\d+|\?)\s*,\s*(\d+|\?)`, "write LIMIT n OFFSET m; LIMIT a, b is SQLite-only"),
	r("jsonb-operator", `(^|[^?])\?[|&]`, "?| and ?& are Postgres JSONB operators and collide with the placeholder; use the Dialect JSON helpers"),
}

var likeRe = regexp.MustCompile(`(?i)\bLIKE\b`)

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
		if likeNeedsCase(m, loc) {
			out = append(out, Finding{Rule: "like-case", Message: "LIKE is case-insensitive on SQLite and case-sensitive on Postgres; write lower(col) LIKE lower(?)", SQL: short(query)})
			break
		}
	}
	return out
}

var (
	notTail     = regexp.MustCompile(`(?i)\s+NOT$`)
	lowerTail   = regexp.MustCompile(`(?i)(?:gostore_)?lower\s*\((?:[^()]|\([^()]*\))*\)$`)
	lowerHeadRe = regexp.MustCompile(`(?i)^(?:gostore_)?lower\s*\(`)
)

// likeNeedsCase reports whether the LIKE at loc lacks the lower() pair. The left
// operand decides: if it is built at run time nothing can be said, if it is
// lower(...) the right must be lower(...) or built at run time, anything else
// is a finding.
func likeNeedsCase(m string, loc []int) bool {
	before := strings.TrimSpace(notTail.ReplaceAllString(strings.TrimSpace(m[:loc[0]]), ""))
	after := strings.TrimSpace(m[loc[1]:])
	switch {
	case strings.HasSuffix(before, unknown):
		return false
	case lowerTail.MatchString(before):
		return !(lowerHeadRe.MatchString(after) || strings.HasPrefix(after, unknown))
	}
	return true
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
	waiversAt := func(line int) []string {
		var out []string
		for _, l := range []int{line, line - 1} {
			if ws, ok := waived[l]; ok {
				if len(ws) == 0 {
					return []string{"*"}
				}
				out = append(out, ws...)
			}
		}
		return out
	}
	pkgEnv := constStrings(f, fset, waiversAt)
	env := pkgEnv
	vars := map[string]string{} // variables that hold SQL, with the text so far
	var out []Finding
	// addNew reports the findings of full that are not already present in prior
	// (the text the variables or constants it was built from), and honours the
	// waivers carried by the constants it used.
	addNew := func(full, prior string, carried []string, pos token.Pos) {
		had := map[string]bool{}
		if prior != "" {
			for _, fd := range Check(prior) {
				had[fd.Rule] = true
			}
		}
		for _, fd := range Check(full) {
			if had[fd.Rule] || slices.Contains(carried, "*") || slices.Contains(carried, fd.Rule) {
				continue
			}
			fd.File = filename
			fd.Line = fset.Position(pos).Line
			if !isWaived(fd.Rule, fd.Line) {
				out = append(out, fd)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			// Variables and local constants do not leak between functions.
			vars = map[string]string{}
			env = pkgEnv
			if x.Body != nil {
				env = withLocalConsts(pkgEnv, x.Body, fset, waiversAt)
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			str, err := strconv.Unquote(x.Value)
			if err != nil || !sqlStart.MatchString(str) {
				return true
			}
			addNew(str, "", nil, x.Pos())
		case *ast.BinaryExpr:
			// SQL built as "SELECT ..." + "FROM ...": fold the chain so a rule
			// that spans the pieces still sees one statement. Same-file string
			// constants and SQL-holding variables are resolved; any other operand
			// is unknown.
			if x.Op != token.ADD {
				return true
			}
			fv := foldString(x, env, vars)
			if fv.lits == 0 || !(sqlStart.MatchString(fv.text) || fv.viaVar) {
				return true
			}
			addNew(fv.text, strings.Join(fv.prior, ""), fv.waive, x.Pos())
			return false
		case *ast.AssignStmt:
			// q := "SELECT ..."; q += " WHERE x ILIKE ?": a variable that starts as
			// SQL has its appended pieces checked with what came before them.
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return true
			}
			id, ok := x.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			fv := foldString(x.Rhs[0], env, vars)
			switch {
			case x.Tok == token.ADD_ASSIGN && vars[id.Name] != "" && fv.lits > 0:
				prev := vars[id.Name]
				addNew(prev+fv.text, prev+strings.Join(fv.prior, ""), fv.waive, x.Pos())
				vars[id.Name] = prev + fv.text
				return false
			case (x.Tok == token.DEFINE || x.Tok == token.ASSIGN) && fv.lits > 0 && (sqlStart.MatchString(fv.text) || fv.viaVar):
				// Check here, against what the pieces already were, and do not
				// descend: the variable's own text is about to change.
				addNew(fv.text, strings.Join(fv.prior, ""), fv.waive, x.Pos())
				vars[id.Name] = fv.text
				return false
			case (x.Tok == token.DEFINE || x.Tok == token.ASSIGN) && vars[id.Name] != "":
				delete(vars, id.Name) // reassigned to something that is not SQL
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "LastInsertId" {
				fd := Finding{Rule: "last-insert-id", Message: "LastInsertId is not portable; use INSERT ... RETURNING id", SQL: "LastInsertId()", File: filename, Line: fset.Position(x.Pos()).Line}
				if !isWaived(fd.Rule, fd.Line) {
					out = append(out, fd)
				}
			}
		}
		return true
	})
	return out, nil
}

// folded is a string expression reduced to text.
type folded struct {
	text   string
	lits   int      // literal and constant operands folded
	viaVar bool     // an operand was a variable that holds SQL
	prior  []string // the text of those variables (already checked when they were built)
	waive  []string // waivers carried by constants used ("*" for all rules)
}

// constVal is a resolved string constant with the waivers on its declaration.
type constVal struct {
	text  string
	waive []string
}

// foldString joins a chain of "+" operands into one string. Same-file string
// constants and SQL-holding variables are resolved; other operands become
// [unknown].
func foldString(e ast.Expr, env map[string]constVal, vars map[string]string) folded {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if s, err := strconv.Unquote(x.Value); err == nil {
				return folded{text: s, lits: 1}
			}
		}
	case *ast.Ident:
		if v, ok := env[x.Name]; ok {
			f := folded{text: v.text, lits: 1, waive: v.waive}
			if sqlStart.MatchString(v.text) {
				f.prior = []string{v.text} // a constant that is SQL is checked where it is declared
			}
			return f
		}
		if t, ok := vars[x.Name]; ok {
			return folded{text: t, viaVar: true, prior: []string{t}}
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			l, r := foldString(x.X, env, vars), foldString(x.Y, env, vars)
			return folded{text: l.text + r.text, lits: l.lits + r.lits, viaVar: l.viaVar || r.viaVar,
				prior: append(l.prior, r.prior...), waive: append(l.waive, r.waive...)}
		}
	case *ast.ParenExpr:
		return foldString(x.X, env, vars)
	}
	return folded{text: unknown}
}

// constStrings collects the package-level string constants (and variables with
// a literal value) of a file, with the waivers written on their declarations.
func constStrings(f *ast.File, fset *token.FileSet, waiversAt func(int) []string) map[string]constVal {
	env := map[string]constVal{}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok {
			collectConsts(gd, env, fset, waiversAt)
		}
	}
	return env
}

// withLocalConsts extends env with the constants declared inside one function.
func withLocalConsts(env map[string]constVal, body *ast.BlockStmt, fset *token.FileSet, waiversAt func(int) []string) map[string]constVal {
	local := map[string]constVal{}
	for k, v := range env {
		local[k] = v
	}
	for _, st := range body.List {
		if ds, ok := st.(*ast.DeclStmt); ok {
			if gd, ok := ds.Decl.(*ast.GenDecl); ok {
				collectConsts(gd, local, fset, waiversAt)
			}
		}
	}
	return local
}

func collectConsts(gd *ast.GenDecl, env map[string]constVal, fset *token.FileSet, waiversAt func(int) []string) {
	for pass := 0; pass < 3; pass++ { // a const may refer to a later one
		for _, sp := range gd.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			ws := waiversAt(fset.Position(vs.Pos()).Line)
			for i, name := range vs.Names {
				if fv := foldString(vs.Values[i], env, nil); fv.lits > 0 && !strings.Contains(fv.text, unknown) {
					env[name.Name] = constVal{text: fv.text, waive: append(append([]string(nil), fv.waive...), ws...)}
				}
			}
		}
	}
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
