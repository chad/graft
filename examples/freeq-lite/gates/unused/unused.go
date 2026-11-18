// Command unused is a freeq-lite gate: it fails when a top-level function is
// never referenced. In this architecture every function is reachable from
// main or from a handler registered in init(), so an unreferenced one is dead
// code, and in practice dead code is a dropped requirement: the #14 probe's
// second attempt left topicsSendOnJoin uncalled ("channels.go must call it")
// and still reported success. This turns that silent drop into a refusal.
//
// Known limitation: a function referenced only by itself (recursion) counts
// as used. Methods are not checked (interface satisfaction makes "unused"
// ambiguous for them).
//
// Run in the generated tree: go run <path>/unused.go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	files, _ := filepath.Glob("*.go")
	sort.Strings(files)
	fset := token.NewFileSet()
	declared := map[string]string{} // func name -> "file:line"
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				switch fd.Name.Name {
				case "main", "init", "_":
				default:
					declared[fd.Name.Name] = fmt.Sprintf("%s:%d", f, fset.Position(fd.Pos()).Line)
				}
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				// The declaration's own name is not a use; walk its body.
				if x.Body != nil {
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if id, ok := m.(*ast.Ident); ok {
							used[id.Name] = true
						}
						return true
					})
				}
				if x.Type != nil {
					ast.Inspect(x.Type, func(m ast.Node) bool {
						if id, ok := m.(*ast.Ident); ok {
							used[id.Name] = true
						}
						return true
					})
				}
				return false
			case *ast.Ident:
				used[x.Name] = true
			}
			return true
		})
	}
	var dead []string
	for name, at := range declared {
		if !used[name] {
			dead = append(dead, fmt.Sprintf("%s: func %s is never called; if a requirement needs it, the code that should call it is missing", at, name))
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		fmt.Println(strings.Join(dead, "\n"))
		os.Exit(1)
	}
	fmt.Printf("unused ok: %d functions, all referenced\n", len(declared))
}
