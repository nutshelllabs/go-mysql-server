package main

import (
	"go/ast"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// likeescapeAnalyzer forbids re-scanning LIKE patterns with strings.Count
// on wildcard literals. The wildcard decision is made during parsing and
// arrives on the expression; recounting here reintroduces the escaping
// bugs it replaced.
var likeescapeAnalyzer = &analysis.Analyzer{
	Name: "likeescape",
	Doc:  "check LIKE patterns are not re-scanned with strings.Count",
	Run:  runLikeescape,
}

func runLikeescape(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		if strings.HasSuffix(pass.Fset.Position(file.Pos()).Filename, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Count" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "strings" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			switch val {
			case "_", "%", `\_`, `\%`:
				pass.Reportf(call.Pos(), "do not recount LIKE wildcards with strings.Count; use the parse-time decision")
			}
			return true
		})
	}
	return nil, nil
}
