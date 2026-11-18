// Command unused is a freeq-lite gate: it fails when a package-level function
// is never used. In this architecture every function is reachable from main or
// from a handler registered in init(), so an unused one is dead code, and in
// practice dead code is a dropped requirement: the #14 probe's second attempt
// left topicsSendOnJoin uncalled ("channels.go must call it") and still
// reported success. This turns that silent drop into a refusal.
//
// It type-checks the package (go/types) and walks the call graph from the
// roots (main, init, initializers that run code, _test.go files, method
// bodies), so: a same-named method is not a use; `var _ = f` and `_ = f` are
// not uses; self- and mutual recursion among otherwise unreached functions are
// reported. Methods themselves are not checked: interface satisfaction makes
// "unused" ambiguous for them.
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

	// Package-level functions, and a call graph between them: an edge from
	// each function (or package-level initializer) to every package-level
	// function it uses. A function is live if it is reachable from a root:
	// main, init, any initializer that runs code, or a _test.go file.
	funcs := map[*types.Func]token.Pos{}
	edges := map[*types.Func][]*types.Func{}
	var roots []*types.Func
	rootSet := map[*types.Func]bool{}
	addRoot := func(fn *types.Func) {
		if fn != nil && !rootSet[fn] {
			rootSet[fn] = true
			roots = append(roots, fn)
		}
	}
	// A synthetic root collects uses from initializers and tests.
	initRoot := types.NewFunc(token.NoPos, pkg, "<initializers and tests>", nil)
	addRoot(initRoot)
	// `_ = f` only silences the compiler; the f there is not a use.
	silenced := map[*ast.Ident]bool{}
	for _, f := range files {
		ast.Inspect(f, func(m ast.Node) bool {
			if as, ok := m.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
						if r, ok := as.Rhs[i].(*ast.Ident); ok {
							silenced[r] = true
						}
					}
				}
			}
			return true
		})
	}
	usesIn := func(n ast.Node) []*types.Func {
		var out []*types.Func
		ast.Inspect(n, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && !silenced[id] {
				if fn, ok := info.Uses[id].(*types.Func); ok && fn.Pkg() == pkg && fn.Type().(*types.Signature).Recv() == nil {
					out = append(out, fn)
				}
			}
			return true
		})
		return out
	}
	for i, f := range files {
		test := strings.HasSuffix(names[i], "_test.go")
		for _, d := range f.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				if x.Recv != nil {
					// Method bodies run when the method is called; treat them as
					// reachable roots (methods are not checked themselves).
					edges[initRoot] = append(edges[initRoot], usesIn(x.Body)...)
					continue
				}
				fn, _ := info.Defs[x.Name].(*types.Func)
				if fn == nil {
					continue
				}
				if test || x.Name.Name == "main" || x.Name.Name == "init" {
					addRoot(fn)
				} else {
					funcs[fn] = x.Pos()
				}
				edges[fn] = append(edges[fn], usesIn(x)...)
			case *ast.GenDecl:
				if x.Tok != token.VAR {
					continue
				}
				// A non-blank var initializer is live code. A blank one
				// (`var _ = f`) only silences the compiler unless it actually
				// calls something, e.g. `var _ = func(){ f() }()`.
				if blankOnly(x) && !callsSomething(x) {
					continue
				}
				edges[initRoot] = append(edges[initRoot], usesIn(x)...)
			}
		}
	}
	reach := map[*types.Func]bool{}
	queue := append([]*types.Func{}, roots...)
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if reach[fn] {
			continue
		}
		reach[fn] = true
		queue = append(queue, edges[fn]...)
	}
	used := reach
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

func callsSomething(g *ast.GenDecl) bool {
	found := false
	ast.Inspect(g, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			found = true
		}
		return !found
	})
	return found
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
