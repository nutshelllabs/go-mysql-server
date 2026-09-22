package main

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// docnameAnalyzer requires exported declaration docs to start with the
// declared name, per the Godoc convention.
var docnameAnalyzer = &analysis.Analyzer{
	Name: "docname",
	Doc:  "check exported declaration docs start with the declared name",
	Run:  runDocname,
}

func runDocname(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Doc == nil || !ast.IsExported(decl.Name.Name) {
					continue
				}
				if firstWord(decl.Doc.Text()) != decl.Name.Name {
					pass.Reportf(decl.Pos(), "doc comment should start with %q", decl.Name.Name)
				}
			case *ast.GenDecl:
				if decl.Doc == nil {
					continue
				}
				names := exportedSpecNames(decl)
				if len(names) != 1 {
					continue
				}
				if firstWord(decl.Doc.Text()) != names[0] {
					pass.Reportf(decl.Pos(), "doc comment should start with %q", names[0])
				}
			}
		}
	}
	return nil, nil
}

// exportedSpecNames lists the exported names a GenDecl declares.
func exportedSpecNames(decl *ast.GenDecl) []string {
	var names []string
	for _, spec := range decl.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			if ast.IsExported(spec.Name.Name) {
				names = append(names, spec.Name.Name)
			}
		case *ast.ValueSpec:
			for _, name := range spec.Names {
				if ast.IsExported(name.Name) {
					names = append(names, name.Name)
				}
			}
		}
	}
	return names
}

// firstWord reports the first word of |text|.
func firstWord(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
