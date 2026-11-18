// Command unused is a freeq-lite gate: it fails when a package-level function
// is unreachable. In this architecture every function is reachable from main
// or from a handler registered in init(), so an unreachable one is dead code,
// and in practice dead code is a dropped requirement: the #14 probe's second
// attempt left topicsSendOnJoin uncalled ("channels.go must call it") and
// still reported success. This turns that silent drop into a refusal.
//
// It type-checks the package (go/types, honouring build tags) and walks a
// graph of package-level functions, methods and variables:
//
//   - roots: main, init, everything in _test.go files, and the calls an
//     initializer makes (they run at program start);
//   - a variable is live only if live code reads it, so a function stored in
//     a never-read global map is dead;
//   - a method is live if live code selects it, or if its name is a method of
//     a common interface (String, Error, Read, Write, Close, ServeHTTP, Len,
//     Less, Swap, MarshalJSON, UnmarshalJSON), since calls through an
//     interface are not visible statically;
//   - `_ = f`, `_ = (f)` and `var _ = f` silence the compiler and are not uses.
//
// Run in the generated tree: go run <path>/unused.go
package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var interfaceMethods = map[string]bool{
	"String": true, "Error": true, "Read": true, "Write": true, "Close": true, "ServeHTTP": true,
	"Len": true, "Less": true, "Swap": true, "MarshalJSON": true, "UnmarshalJSON": true,
}

func main() {
	all, _ := filepath.Glob("*.go")
	sort.Strings(all)
	fset := token.NewFileSet()
	var files []*ast.File
	var names []string
	for _, n := range all {
		if ok, err := build.Default.MatchFile(".", n); err != nil || !ok {
			continue // excluded by build tags or file name
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			fail(err.Error())
		}
		files = append(files, f)
		names = append(names, n)
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil), Error: func(error) {}}
	pkg, _ := conf.Check(".", fset, files, info)
	if pkg == nil {
		fail("type-check failed")
	}

	// Identifiers that only silence the compiler: `_ = f` and `_ = (f)`.
	silenced := map[*ast.Ident]bool{}
	for _, f := range files {
		ast.Inspect(f, func(m ast.Node) bool {
			if as, ok := m.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
						if r, ok := unparen(as.Rhs[i]).(*ast.Ident); ok {
							silenced[r] = true
						}
					}
				}
			}
			return true
		})
	}
	refs := func(n ast.Node) []types.Object {
		var out []types.Object
		ast.Inspect(n, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && !silenced[id] {
				if o := info.Uses[id]; o != nil && (isPkgLevel(o, pkg) || isMethodOf(o, pkg)) {
					out = append(out, o)
				}
			}
			return true
		})
		return out
	}
	// calls returns the package-level functions/methods an initializer calls
	// (directly, or inside a function literal it invokes): they run at start.
	calls := func(n ast.Node) []types.Object {
		var out []types.Object
		ast.Inspect(n, func(m ast.Node) bool {
			if c, ok := m.(*ast.CallExpr); ok {
				switch fn := unparen(c.Fun).(type) {
				case *ast.FuncLit:
					out = append(out, refs(fn.Body)...)
				case *ast.Ident:
					if o := info.Uses[fn]; o != nil && isPkgLevel(o, pkg) {
						out = append(out, o)
					}
				case *ast.SelectorExpr:
					if o := info.Uses[fn.Sel]; o != nil && isMethodOf(o, pkg) {
						out = append(out, o)
					}
				}
			}
			return true
		})
		return out
	}

	start := types.NewFunc(token.NoPos, pkg, "<program start>", nil)
	edges := map[types.Object][]types.Object{}
	candidates := map[*types.Func]token.Pos{}
	for i, f := range files {
		test := strings.HasSuffix(names[i], "_test.go")
		for _, d := range f.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				obj := info.Defs[x.Name]
				if obj == nil {
					continue
				}
				edges[obj] = append(edges[obj], refs(x)...)
				switch {
				case test || (x.Recv == nil && (x.Name.Name == "main" || x.Name.Name == "init")):
					edges[start] = append(edges[start], obj)
				case x.Recv != nil:
					if interfaceMethods[x.Name.Name] {
						edges[start] = append(edges[start], obj)
					}
				default:
					if fn, ok := obj.(*types.Func); ok {
						candidates[fn] = x.Pos()
					}
				}
			case *ast.GenDecl:
				if x.Tok != token.VAR {
					continue
				}
				for _, sp := range x.Specs {
					vs := sp.(*ast.ValueSpec)
					for _, v := range vs.Values {
						if test {
							edges[start] = append(edges[start], refs(v)...)
							continue
						}
						edges[start] = append(edges[start], calls(v)...) // runs at start
						for _, nm := range vs.Names {
							if nm.Name == "_" {
								continue // `var _ = f` silences; it is not a use
							}
							if o := info.Defs[nm]; o != nil {
								edges[o] = append(edges[o], refs(v)...)
							}
						}
					}
				}
			}
		}
	}
	reach := map[types.Object]bool{}
	queue := []types.Object{start}
	for len(queue) > 0 {
		o := queue[0]
		queue = queue[1:]
		if reach[o] {
			continue
		}
		reach[o] = true
		queue = append(queue, edges[o]...)
	}
	var dead []string
	for fn, pos := range candidates {
		if !reach[fn] {
			p := fset.Position(pos)
			dead = append(dead, fmt.Sprintf("%s:%d: func %s is never called; if a requirement needs it, the code that should call it is missing", filepath.Base(p.Filename), p.Line, fn.Name()))
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		fmt.Println(strings.Join(dead, "\n"))
		os.Exit(1)
	}
	fmt.Printf("unused ok: %d functions, all reachable\n", len(candidates))
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// isPkgLevel: a package-level func or var of pkg (not a local, field or method).
func isPkgLevel(o types.Object, pkg *types.Package) bool {
	if o.Pkg() != pkg || o.Parent() != pkg.Scope() {
		return false
	}
	switch o.(type) {
	case *types.Func, *types.Var:
		return true
	}
	return false
}

func isMethodOf(o types.Object, pkg *types.Package) bool {
	fn, ok := o.(*types.Func)
	if !ok || fn.Pkg() != pkg {
		return false
	}
	sig, _ := fn.Type().(*types.Signature)
	return sig != nil && sig.Recv() != nil
}

func fail(msg string) {
	fmt.Println(msg)
	os.Exit(1)
}
