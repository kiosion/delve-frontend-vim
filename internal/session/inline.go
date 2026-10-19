package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"

	"github.com/go-delve/delve/service/api"
)

const maxInlineValue = 60

// Inline is the values of the variables that a source line refers to.
type Inline struct {
	// Line is the 1-based line in the source file of the frame.
	Line int `json:"line"`
	// Text lists the variables as "name = value" pairs.
	Text string `json:"text"`
}

// inlineValues returns the values of vars on each line of the function around path:line,
// from the start of the function to line. An identifier shows the value of the variable
// it resolves to, matched by the line of its declaration.
func inlineValues(path string, line int, vars []api.Variable) []Inline {
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if file == nil {
		return nil
	}
	// Checking the file alone resolves the locals of a function. The errors from
	// imports and from declarations in other files do not affect them.
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Error: func(error) {}}
	conf.Check(file.Name.Name, fset, []*ast.File{file}, info)
	lineOf := func(pos token.Pos) int { return fset.Position(pos).Line }
	var fn ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			if lineOf(n.Pos()) <= line && line <= lineOf(n.End()) {
				fn = n
			}
		}
		return true
	})
	if fn == nil {
		return nil
	}

	type shownKey struct {
		line int
		obj  types.Object
	}
	var inline []Inline
	index := map[int]int{}
	shown := map[shownKey]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj, ok := info.ObjectOf(id).(*types.Var)
		if !ok || obj.IsField() {
			return true
		}
		l := lineOf(id.Pos())
		key := shownKey{l, obj}
		if l > line || shown[key] {
			return true
		}
		v := variable(vars, id.Name, lineOf(obj.Pos()))
		if v == nil {
			return true
		}
		shown[key] = true
		text := []rune(value(v))
		if len(text) > maxInlineValue {
			text = append(text[:maxInlineValue], []rune("...")...)
		}
		i, ok := index[l]
		if !ok {
			i = len(inline)
			index[l] = i
			inline = append(inline, Inline{Line: l})
		}
		inline[i].Text = strings.TrimPrefix(inline[i].Text+", "+id.Name+" = "+string(text), ", ")
		return true
	})
	return inline
}

// variable returns the variable in vars named name and declared on line declLine. A
// variable without a declaration line matches by name alone.
func variable(vars []api.Variable, name string, declLine int) *api.Variable {
	for i, v := range vars {
		if v.Name == name && (v.DeclLine == int64(declLine) || v.DeclLine == 0) {
			return &vars[i]
		}
	}
	return nil
}
