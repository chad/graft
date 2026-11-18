// Command registry is a freeq-lite gate: it enforces the conventions' rule
// that each file registers only its own commands. Without it, a requirement
// that spans regions can be silently absorbed into the newest region (for
// example, a Topics file wrapping handlers["JOIN"] instead of the Channels
// record being revised). With it, that change fails to converge and names the
// file that owns the command, so the cross-region edit surfaces.
//
// Run in the generated tree: go run <path>/registry.go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	files, _ := filepath.Glob("*.go")
	sort.Strings(files)
	fset := token.NewFileSet()
	owner := map[string]string{}
	var problems []string
	parsed := map[string]*ast.File{}
	declarer := ""
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		parsed[f] = af
		for _, d := range af.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
				for _, sp := range g.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						if n.Name == "handlers" {
							declarer = f
						}
					}
				}
			}
		}
	}
	for _, f := range files {
		af := parsed[f]
		if af == nil {
			continue
		}
		// Outside the file that declares the registry, the only permitted use of
		// the name is a direct registration: handlers["CMD"] = <handler>.
		// Aliases, parenthesised forms, passing the map anywhere (reflect,
		// helpers) and reassigning the whole map are all refused.
		if f != declarer && f != strings.TrimSuffix(declarer, ".go")+"_synth.go" {
			allowed := map[*ast.Ident]bool{}
			ast.Inspect(af, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && as.Tok == token.ASSIGN {
					for _, lhs := range as.Lhs {
						if ix, ok := lhs.(*ast.IndexExpr); ok {
							if id, ok := ix.X.(*ast.Ident); ok && id.Name == "handlers" {
								allowed[id] = true
							}
						}
					}
				}
				return true
			})
			ast.Inspect(af, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "handlers" && !allowed[id] && id.Obj == nil {
					problems = append(problems, fmt.Sprintf("%s:%d: handlers may only be used as handlers[\"CMD\"] = ... outside %s", f, fset.Position(id.Pos()).Line, declarer))
				}
				return true
			})
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					key, ok, literal := handlersKey(lhs)
					if !ok {
						continue
					}
					pos := fset.Position(lhs.Pos())
					if !literal {
						problems = append(problems, fmt.Sprintf("%s:%d: handlers is assigned with a non-literal key; register each command by name", f, pos.Line))
						continue
					}
					if prev, taken := owner[key]; taken && prev != f {
						problems = append(problems, fmt.Sprintf("%s:%d: handlers[%q] is already registered by %s; a file may register only its own commands (if this behaviour belongs to that command, the record that owns %s must change)", f, pos.Line, key, prev, prev))
						continue
					}
					owner[key] = f
				}
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "delete" && len(x.Args) > 0 {
					if a, ok := x.Args[0].(*ast.Ident); ok && a.Name == "handlers" {
						problems = append(problems, fmt.Sprintf("%s:%d: handlers entries must not be deleted", f, fset.Position(x.Pos()).Line))
					}
				}
			}
			return true
		})
	}
	if len(problems) > 0 {
		fmt.Println(strings.Join(problems, "\n"))
		os.Exit(1)
	}
	fmt.Printf("registry ok: %d commands, each registered by exactly one file\n", len(owner))
}

// handlersKey reports whether e is handlers[...] and, if the key is a string
// literal, returns it upper-cased.
func handlersKey(e ast.Expr) (key string, isHandlers, literal bool) {
	ix, ok := e.(*ast.IndexExpr)
	if !ok {
		return "", false, false
	}
	id, ok := ix.X.(*ast.Ident)
	if !ok || id.Name != "handlers" {
		return "", false, false
	}
	lit, ok := ix.Index.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", true, false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", true, false
	}
	return strings.ToUpper(s), true, true
}
