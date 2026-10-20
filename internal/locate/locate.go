// Package locate finds the tests in a Go test file and the go test flags that select
// each one.
package locate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Test is a test function, subtest or table case that go test can select by name.
type Test struct {
	// Name is the full name go test reports, such as "TestParse/empty_input".
	Name string `json:"name"`
	// Line is the 1-based line where the test starts.
	Line int `json:"line"`
	// EndLine is the 1-based line where the test ends.
	EndLine int `json:"end_line"`
	// Args are the go test flags that run this test and its subtests.
	Args []string `json:"args"`
}

// File returns the tests in src, the content of the Go file at path. It looks up the
// types that src uses but does not declare in the other files of the package directory.
// When src fails to parse, File returns the tests declared before the error, and a nil
// error.
func File(path string, src []byte) ([]Test, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution|parser.ParseComments)
	if file == nil {
		return nil, err
	}
	l := &locator{
		fset:     fset,
		path:     path,
		pkg:      file.Name.Name,
		types:    map[string]ast.Expr{},
		vars:     map[string]ast.Expr{},
		seen:     map[string]int{},
		comments: file.Comments,
	}
	l.declare(file, true)
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			l.function(fn)
		}
	}
	return l.tests, nil
}

type flags func(pattern string) []string

func runFlags(pattern string) []string {
	return []string{"-run", pattern}
}

func benchFlags(pattern string) []string {
	return []string{"-run", "^$", "-bench", pattern}
}

type locator struct {
	fset  *token.FileSet
	path  string
	pkg   string
	types map[string]ast.Expr
	// vars holds the package-level variables of the file itself. A table in another
	// file has no lines in this one to mark.
	vars         map[string]ast.Expr
	siblingsRead bool
	seen         map[string]int
	comments     []*ast.CommentGroup
	tests        []Test
}

func (l *locator) declare(file *ast.File, withVars bool) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				l.types[spec.Name.Name] = spec.Type
			case *ast.ValueSpec:
				if !withVars {
					continue
				}
				for i, name := range spec.Names {
					if i < len(spec.Values) {
						l.vars[name.Name] = spec.Values[i]
					}
				}
			}
		}
	}
}

func (l *locator) declareSiblingTypes() {
	if l.siblingsRead {
		return
	}
	l.siblingsRead = true
	dir := filepath.Dir(l.path)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() || filepath.Ext(path) != ".go" || path == l.path {
			continue
		}
		file, _ := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if file != nil && file.Name.Name == l.pkg {
			l.declare(file, false)
		}
	}
}

// resolve follows type names to the type they declare.
func (l *locator) resolve(t ast.Expr) ast.Expr {
	for range 8 {
		ident, ok := t.(*ast.Ident)
		if !ok {
			return t
		}
		next, ok := l.types[ident.Name]
		if !ok {
			l.declareSiblingTypes()
			if next, ok = l.types[ident.Name]; !ok {
				return t
			}
		}
		t = next
	}
	return t
}

func (l *locator) function(fn *ast.FuncDecl) {
	if fn.Recv != nil || fn.Body == nil {
		return
	}
	name := fn.Name.Name
	param, kind := testParam(fn.Type)
	var f flags
	switch {
	case isTestName(name, "Test") && kind == "T",
		isTestName(name, "Fuzz") && kind == "F",
		isTestName(name, "Example") && fn.Type.Params.NumFields() == 0 && l.hasOutput(fn.Body):
		f = runFlags
	case isTestName(name, "Benchmark") && kind == "B":
		f = benchFlags
	default:
		return
	}
	l.add(name, fn.Pos(), fn.End(), f)
	if kind == "T" || kind == "B" {
		l.subtests(fn.Body, nil, param, name, f)
	}
}

// subtests adds the subtests that tvar.Run starts in body. outer holds the nodes that
// enclose body, where a table used inside body may be declared.
func (l *locator) subtests(body *ast.BlockStmt, outer []ast.Node, tvar, parent string, f flags) {
	if tvar == "" || tvar == "_" {
		return
	}
	stack := append([]ast.Node(nil), outer...)
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok || !isRun(call, tvar) {
			return true
		}
		l.run(call, stack, parent, f)
		// Inspect skips the closing nil call for a node whose children it skips.
		stack = stack[:len(stack)-1]
		return false
	})
}

func (l *locator) run(call *ast.CallExpr, stack []ast.Node, parent string, f flags) {
	if s, ok := stringValue(call.Args[0]); ok {
		name := l.unique(parent, rewrite(s))
		l.add(name, call.Pos(), call.End(), f)
		if lit, ok := call.Args[1].(*ast.FuncLit); ok {
			tvar, _ := testParam(lit.Type)
			l.subtests(lit.Body, stack, tvar, name, f)
		}
		return
	}
	for _, c := range l.cases(call.Args[0], stack) {
		l.add(l.unique(parent, rewrite(c.name)), c.pos, c.end, f)
	}
}

type tableCase struct {
	name     string
	pos, end token.Pos
}

// cases returns the table entries that a t.Run name argument takes its values from, when
// the call sits in a range loop over a table literal.
func (l *locator) cases(name ast.Expr, stack []ast.Node) []tableCase {
	var ident, field string
	switch e := ast.Unparen(name).(type) {
	case *ast.Ident:
		ident = e.Name
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return nil
		}
		ident, field = x.Name, e.Sel.Name
	default:
		return nil
	}
	for i := len(stack) - 1; i >= 0; i-- {
		loop, ok := stack[i].(*ast.RangeStmt)
		if !ok {
			continue
		}
		fromKey := identName(loop.Key) == ident
		if !fromKey && identName(loop.Value) != ident {
			continue
		}
		table := l.table(loop, stack[:i])
		if table == nil {
			return nil
		}
		return l.entries(table, fromKey, field)
	}
	return nil
}

func (l *locator) table(loop *ast.RangeStmt, stack []ast.Node) *ast.CompositeLit {
	switch x := ast.Unparen(loop.X).(type) {
	case *ast.CompositeLit:
		return x
	case *ast.Ident:
		if v := localValue(x.Name, loop.Pos(), stack); v != nil {
			return compositeLit(v)
		}
		if v, ok := l.vars[x.Name]; ok {
			return compositeLit(v)
		}
	}
	return nil
}

func (l *locator) entries(table *ast.CompositeLit, fromKey bool, field string) []tableCase {
	var elem ast.Expr
	switch t := l.resolve(table.Type).(type) {
	case *ast.ArrayType:
		if fromKey {
			return nil
		}
		elem = t.Elt
	case *ast.MapType:
		elem = t.Value
	default:
		return nil
	}
	var cases []tableCase
	for _, elt := range table.Elts {
		key, value := ast.Expr(nil), elt
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			key, value = kv.Key, kv.Value
		}
		src := value
		switch {
		case fromKey:
			src = key
		case field != "":
			src = l.field(value, elem, field)
		}
		if src == nil {
			continue
		}
		if name, ok := stringValue(src); ok {
			cases = append(cases, tableCase{name: name, pos: elt.Pos(), end: elt.End()})
		}
	}
	return cases
}

// field returns the expression that a struct literal of type elem sets field to.
func (l *locator) field(value, elem ast.Expr, field string) ast.Expr {
	if u, ok := value.(*ast.UnaryExpr); ok && u.Op == token.AND {
		value = u.X
	}
	lit := compositeLit(value)
	if lit == nil || len(lit.Elts) == 0 {
		return nil
	}
	if lit.Type != nil {
		elem = lit.Type
	}
	if _, keyed := lit.Elts[0].(*ast.KeyValueExpr); keyed {
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok && identName(kv.Key) == field {
				return kv.Value
			}
		}
		return nil
	}
	elem = l.resolve(elem)
	if star, ok := elem.(*ast.StarExpr); ok {
		elem = l.resolve(star.X)
	}
	st, ok := elem.(*ast.StructType)
	if !ok {
		return nil
	}
	i := fieldIndex(st, field)
	if i < 0 || i >= len(lit.Elts) {
		return nil
	}
	return lit.Elts[i]
}

// unique returns the name the testing package gives a subtest, which numbers repeated
// names: "a", "a#01", "a#02".
func (l *locator) unique(parent, sub string) string {
	name := parent + "/" + sub
	empty := sub == ""
	for {
		next, exists := l.seen[name]
		if !empty && !exists {
			l.seen[name] = 1
			return name
		}
		l.seen[name] = next + 1
		name = fmt.Sprintf("%s#%02d", name, next)
		empty = false
	}
}

func (l *locator) add(name string, pos, end token.Pos, f flags) {
	l.tests = append(l.tests, Test{
		Name:    name,
		Line:    l.fset.Position(pos).Line,
		EndLine: l.fset.Position(end).Line,
		Args:    f(pattern(name)),
	})
}

// pattern returns the -run or -bench pattern that selects the test named name.
func pattern(name string) string {
	levels := strings.Split(name, "/")
	for i, level := range levels {
		levels[i] = "^" + regexp.QuoteMeta(level) + "$"
	}
	return strings.Join(levels, "/")
}

// rewrite applies the substitutions that the testing package makes to a subtest name.
func rewrite(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte('_')
		case !strconv.IsPrint(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isTestName reports whether name has prefix and is not a longer word, the rule go test
// applies: TestFoo and Test_foo count, Testable does not.
func isTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// testParam returns the name of a function's only parameter and, when its type is
// *testing.T, *testing.B or *testing.F, that type's name.
func testParam(ft *ast.FuncType) (name, kind string) {
	if ft.Params == nil || len(ft.Params.List) != 1 {
		return "", ""
	}
	field := ft.Params.List[0]
	if len(field.Names) > 1 {
		return "", ""
	}
	if len(field.Names) == 1 {
		name = field.Names[0].Name
	}
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return name, ""
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || identName(sel.X) != "testing" {
		return name, ""
	}
	return name, sel.Sel.Name
}

func isRun(call *ast.CallExpr, tvar string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Run" && identName(sel.X) == tvar && len(call.Args) == 2
}

// localValue returns the value last assigned to name, before pos, by a statement in one
// of the blocks in stack.
func localValue(name string, pos token.Pos, stack []ast.Node) ast.Expr {
	for i := len(stack) - 1; i >= 0; i-- {
		block, ok := stack[i].(*ast.BlockStmt)
		if !ok {
			continue
		}
		var found ast.Expr
		for _, stmt := range block.List {
			if stmt.End() > pos {
				break
			}
			if v := assigned(stmt, name); v != nil {
				found = v
			}
		}
		if found != nil {
			return found
		}
	}
	return nil
}

func assigned(stmt ast.Stmt, name string) ast.Expr {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if len(s.Lhs) != len(s.Rhs) {
			return nil
		}
		for i, lhs := range s.Lhs {
			if identName(lhs) == name {
				return s.Rhs[i]
			}
		}
	case *ast.DeclStmt:
		gen, ok := s.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return nil
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == name && i < len(vs.Values) {
					return vs.Values[i]
				}
			}
		}
	}
	return nil
}

func compositeLit(e ast.Expr) *ast.CompositeLit {
	lit, _ := ast.Unparen(e).(*ast.CompositeLit)
	return lit
}

func fieldIndex(st *ast.StructType, name string) int {
	i := 0
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			i++
			continue
		}
		for _, n := range f.Names {
			if n.Name == name {
				return i
			}
			i++
		}
	}
	return -1
}

// stringValue returns the value of a string literal or a sum of string literals.
func stringValue(e ast.Expr) (string, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		x, ok := stringValue(e.X)
		if !ok {
			return "", false
		}
		y, ok := stringValue(e.Y)
		return x + y, ok
	}
	return "", false
}

func identName(e ast.Expr) string {
	if ident, ok := e.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

var outputComment = regexp.MustCompile(`(?i)^[[:space:]]*(unordered )?output:`)

// hasOutput reports whether body holds an output comment. go test compiles an example
// without one, and does not run it.
func (l *locator) hasOutput(body *ast.BlockStmt) bool {
	for _, c := range l.comments {
		if c.Pos() > body.Pos() && c.End() < body.End() && outputComment.MatchString(c.Text()) {
			return true
		}
	}
	return false
}
