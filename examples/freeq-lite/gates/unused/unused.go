// Command unused is a freeq-lite gate: it fails when a package-level function
// is never used. In this architecture every function is reachable from main or
// from a handler registered in init(), so an unused one is dead code, and in
// practice dead code is a dropped requirement: the #14 probe's second attempt
// left topicsSendOnJoin uncalled ("channels.go must call it") and still
// reported success. This turns that silent drop into a refusal.
//
// It type-checks the package (go/types), so uses are resolved precisely: a
// method with the same name is not a use, and `var _ = f` (which exists only
// to silence the compiler) is not a use either. References from _test.go
// files count as uses. Methods are not checked: interface satisfaction makes
// "unused" ambiguous for them. A function used only by itself (recursion) is
// reported as unused.
//
// Run in the generated tree: go run <path>/unused.go
package main

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	names, _ := filepath.Glob("*.go")
	sort.Strings(names)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, n := range names {
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			fail(err.Error())
		}
		files = append(files, f)
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil), Error: func(error) {}}
	pkg, _ := conf.Check(".", fset, files, info)
	if pkg == nil {
		fail("type-check failed")
	}

	// Package-level functions, and the function each identifier sits inside.
	funcs := map[*types.Func]token.Pos{}
	for i, f := range files {
		if strings.HasSuffix(names[i], "_test.go") {
			continue // tests are callers, not candidates
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				switch fd.Name.Name {
				case "main", "init", "_":
					continue
				}
				if obj, ok := info.Defs[fd.Name].(*types.Func); ok {
					funcs[obj] = fd.Pos()
				}
			}
		}
	}
	used := map[*types.Func]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			// `var _ = f` exists to silence "declared and not used"; it is not a use.
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR && blankOnly(g) {
				continue
			}
			var self *types.Func
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				self, _ = info.Defs[fd.Name].(*types.Func)
			}
			ast.Inspect(d, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if fn, ok := info.Uses[id].(*types.Func); ok && fn != self {
					used[fn] = true
				}
				return true
			})
		}
	}
	var dead []string
	for fn, pos := range funcs {
		if !used[fn] {
			p := fset.Position(pos)
			dead = append(dead, fmt.Sprintf("%s:%d: func %s is never called; if a requirement needs it, the code that should call it is missing", filepath.Base(p.Filename), p.Line, fn.Name()))
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		fmt.Println(strings.Join(dead, "\n"))
		os.Exit(1)
	}
	fmt.Printf("unused ok: %d functions, all used\n", len(funcs))
}

func blankOnly(g *ast.GenDecl) bool {
	for _, s := range g.Specs {
		vs, ok := s.(*ast.ValueSpec)
		if !ok {
			return false
		}
		for _, n := range vs.Names {
			if n.Name != "_" {
				return false
			}
		}
	}
	return true
}

func fail(msg string) {
	fmt.Println(msg)
	os.Exit(1)
}
