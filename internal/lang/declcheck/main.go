// Command declcheck is the Go profile's decl_check: it reads the declarations
// of a drafted skeleton from stdin and rejects any that holds logic. graft
// itself parses no target code (SPEC: language-agnostic); this program is
// what the Go profile names as its decl_check, so the Go-specific part lives
// here and a profile for another language supplies its own.
//
// Protocol: stdin is a JSON array of {"file","kind","name","code"}. Exit 0 when
// every declaration is a type, a const or a var without logic. Otherwise one
// line per problem, "file: name: reason", on stderr and exit 1. A bad input is
// exit 2.
//
// Rules (the standard library only, so `go run` never needs the network):
//   - type: any type expression, but no function literal and no call
//     (a call in a type is an array length like [len(x)]int).
//   - const: literals, identifiers, selectors, unary and binary operators and
//     parentheses. No calls, conversions included: time.Second * 5 passes,
//     len("a") does not.
//   - var: no initialiser, or one empty composite literal. Nothing else.
//   - func and method are not declarations here: bodies are never drafted.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
)

type decl struct {
	File string `json:"file"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Code string `json:"code"`
}

func main() {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "declcheck:", err)
		os.Exit(2)
	}
	var ds []decl
	if err := json.Unmarshal(b, &ds); err != nil {
		fmt.Fprintln(os.Stderr, "declcheck: bad input:", err)
		os.Exit(2)
	}
	bad := 0
	for _, d := range ds {
		for _, why := range check(d) {
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", d.File, d.Name, why)
			bad++
		}
	}
	if bad > 0 {
		os.Exit(1)
	}
}

func check(d decl) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "decl.go", "package p\n"+d.Code+"\n", 0)
	if err != nil {
		return []string{"does not parse: " + err.Error()}
	}
	if len(f.Decls) == 0 {
		return []string{"code holds no declaration"}
	}
	var out []string
	for _, dc := range f.Decls {
		gd, ok := dc.(*ast.GenDecl)
		if !ok {
			out = append(out, "a function or method has a body; bodies are never drafted (use a stub)")
			continue
		}
		if gd.Tok == token.IMPORT {
			out = append(out, "an import belongs in the file's imports, not in a decl")
			continue
		}
		if gd.Tok.String() != d.Kind {
			out = append(out, fmt.Sprintf("kind is %q but the code is a %s declaration", d.Kind, gd.Tok))
			continue
		}
		for _, sp := range gd.Specs {
			out = append(out, checkSpec(gd.Tok, sp)...)
		}
	}
	return out
}

func checkSpec(tok token.Token, sp ast.Spec) []string {
	switch s := sp.(type) {
	case *ast.TypeSpec:
		out := noLogic(s.Type, "type")
		if s.TypeParams != nil { // constraints can hold array lengths too
			out = append(out, noLogic(s.TypeParams, "type parameter list")...)
		}
		return out
	case *ast.ValueSpec:
		var out []string
		if s.Type != nil {
			out = append(out, noLogic(s.Type, "type")...)
		}
		for _, v := range s.Values {
			if tok == token.CONST {
				out = append(out, constExpr(v)...)
			} else if cl, ok := v.(*ast.CompositeLit); !ok || len(cl.Elts) > 0 {
				out = append(out, "a var initialiser must be absent or an empty composite literal: "+what(v))
			} else if cl.Type != nil {
				out = append(out, noLogic(cl.Type, "type")...)
			}
		}
		return out
	}
	return nil
}

// noLogic rejects a function literal or a call anywhere in a type expression.
func noLogic(n ast.Node, ctx string) []string {
	var out []string
	ast.Inspect(n, func(x ast.Node) bool {
		switch x.(type) {
		case *ast.FuncLit:
			out = append(out, "a function literal in a "+ctx)
		case *ast.CallExpr:
			out = append(out, "a call in a "+ctx)
		}
		return true
	})
	return out
}

func constExpr(e ast.Expr) []string {
	switch x := e.(type) {
	case *ast.BasicLit, *ast.Ident:
		return nil
	case *ast.SelectorExpr:
		return constExpr(x.X)
	case *ast.ParenExpr:
		return constExpr(x.X)
	case *ast.UnaryExpr:
		return constExpr(x.X)
	case *ast.BinaryExpr:
		return append(constExpr(x.X), constExpr(x.Y)...)
	}
	return []string{"a const may use only literals, names, operators and parentheses (no calls or conversions): " + what(e)}
}

func what(e ast.Expr) string { return fmt.Sprintf("%T", e)[5:] }
