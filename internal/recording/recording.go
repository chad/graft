// Package recording is graft's planning layer (SPEC §2, §5): it turns accepted
// records plus a typed skeleton into a sedum recording, deterministically and
// with no model call. Sedum is invoked to render it (internal/render), never
// imported.
//
// What a record cannot express through catalog actions is not invented here.
// A function whose body is open is rendered as a stub that only delegates to a
// helper. The helpers live in a separate file, <stem>_synth.go, owned entirely
// by one convergence region (SPEC §6): the region's body is the whole file
// (package clause, imports, helpers), so converge can add imports and sedum's
// file templates never see the helpers. Plan seeds each region with helpers
// that panic("graft: not converged"), so the package builds before converge
// runs and each region can be gated on its own.
package recording

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/records"
)

// SedumVersion is written into every recording.
const SedumVersion = "0.12.2"

// Decl is one fixed top-level declaration (type, var, const, or a function
// whose whole text is part of the skeleton). Code is gofmt-formatted Go.
type Decl struct {
	Kind     string `json:"kind"` // type | var | const | func | method
	Name     string `json:"name"`
	Receiver string `json:"receiver,omitempty"` // method only: type name, no star
	Code     string `json:"code"`
}

// Param is a named parameter.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Stub is a function or method whose body is left to convergence.
type Stub struct {
	Name         string   `json:"name"`
	ReceiverName string   `json:"receiver_name,omitempty"` // e.g. "c"
	ReceiverType string   `json:"receiver_type,omitempty"` // e.g. "*Client" or "Server"
	Params       []Param  `json:"params,omitempty"`
	Results      []string `json:"results,omitempty"`
	// Doc is the contract the stub's doc comment states (no comment markers).
	// It is where conventions that every body must agree on live.
	Doc string `json:"doc,omitempty"`
}

// File is one source file owned by one record.
type File struct {
	Path string `json:"path"`
	// Record names the owning record by id; Section names it by the spec
	// section the record was derived from. Exactly one is set.
	Record  string `json:"record,omitempty"`
	Section string `json:"section,omitempty"`
	// Package overrides the synth file's package name ("main" for the module
	// root and cmd/<bin>, the directory name otherwise).
	Package string   `json:"package,omitempty"`
	Imports []string `json:"imports,omitempty"`
	Decls   []Decl   `json:"decls,omitempty"`
	Stubs   []Stub   `json:"stubs,omitempty"`
}

// Skeleton is the typed layout input. Records carry only statements, so the
// declarations come from here.
type Skeleton struct {
	Files []File `json:"files"`
}

// Rec is an accepted record and the spec section it was derived from.
type Rec struct {
	Pin     records.Pin
	Section string
}

// Input is everything Plan reads.
type Input struct {
	Records  []Rec
	Skeleton Skeleton
	// Tier is the risk tier written into every seeded convergence region.
	Tier string
}

// Recording is sedum's on-disk recording.
type Recording struct {
	SedumVersion string             `json:"sedum_version"`
	Packages     map[string]Package `json:"packages"`
	Records      []Record           `json:"records"`
	// Complete says the recording selects every record, so sedum prunes
	// regions no record selects any more (SPEC §4).
	Complete bool `json:"complete,omitempty"`
}

// Package is a package entry of a recording.
type Package struct {
	Extensions []string `json:"extensions"`
}

// Record is one record's slice of a recording.
type Record struct {
	RecordID string  `json:"record_id"`
	Gen      string  `json:"gen"`
	Files    []RFile `json:"files"`
	Phases   []Phase `json:"phases"`
}

// RFile is a file a record authorises.
type RFile struct {
	Path     string            `json:"path"`
	Package  string            `json:"package"`
	Template string            `json:"template"`
	Captures map[string]string `json:"captures"`
}

// Phase is an ordered list of invocations.
type Phase struct {
	Name        string       `json:"name"`
	Invocations []Invocation `json:"invocations"`
}

// Invocation is one catalog action call.
type Invocation struct {
	Action string            `json:"action"`
	Kwargs map[string]string `json:"kwargs"`
}

// Region is a convergence region a record owns. Path is its own file
// (<stem>_synth.go), which sedum never touches. Seed is that file's initial
// content: the whole file is the region. Converge replaces the body, which
// must keep declaring every function in Declares because the rendered stubs
// call them.
type Region struct {
	Path     string
	Owner    records.Pin
	Tier     string
	Declares []string // full signatures, no body
	Seed     []byte
}

// Planned is Plan's result.
type Planned struct {
	Recording Recording
	Regions   []Region
}

// Plan is deterministic: the same input gives a byte-identical recording.
func Plan(in Input) (Planned, error) {
	if in.Tier == "" {
		return Planned{}, errors.New("recording: Input.Tier is required")
	}
	pins := map[string]records.Pin{}
	bySection := map[string]string{}
	for _, r := range in.Records {
		p := r.Pin
		if p.RecordID == "" {
			return Planned{}, errors.New("recording: record without an id")
		}
		if _, dup := pins[p.RecordID]; dup {
			return Planned{}, fmt.Errorf("recording: duplicate record %q", p.RecordID)
		}
		if p.Type != "imprint" {
			return Planned{}, fmt.Errorf("recording: %s is a %q; only imprints own code (SPEC §3)", p.RecordID, p.Type)
		}
		if p.Status != "open" && p.Status != "implemented" {
			return Planned{}, fmt.Errorf("recording: %s has status %q; only open or implemented imprints are planned", p.RecordID, p.Status)
		}
		pins[p.RecordID] = p
		if r.Section != "" {
			if _, dup := bySection[r.Section]; dup {
				return Planned{}, fmt.Errorf("recording: two records for section %q", r.Section)
			}
			bySection[r.Section] = p.RecordID
		}
	}
	byRec := map[string][]File{}
	for _, f := range in.Skeleton.Files {
		var rid string
		switch {
		case f.Record != "" && f.Section != "":
			return Planned{}, fmt.Errorf("recording: file %s sets both record and section", f.Path)
		case f.Record != "":
			if _, ok := pins[f.Record]; !ok {
				return Planned{}, fmt.Errorf("recording: file %s names record %q, which is not planned", f.Path, f.Record)
			}
			rid = f.Record
		case f.Section != "":
			id, ok := bySection[f.Section]
			if !ok {
				return Planned{}, fmt.Errorf("recording: file %s names section %q, which no record covers", f.Path, f.Section)
			}
			rid = id
		default:
			return Planned{}, fmt.Errorf("recording: file %s names neither a record nor a section", f.Path)
		}
		byRec[rid] = append(byRec[rid], f)
	}
	if err := validateSkeleton(in.Skeleton); err != nil {
		return Planned{}, err
	}
	ids := make([]string, 0, len(pins))
	for id := range pins {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := Planned{Recording: Recording{
		SedumVersion: SedumVersion,
		Complete:     true,
		Packages:     map[string]Package{"go": {Extensions: []string{".go"}}},
		Records:      []Record{},
	}}
	for _, id := range ids {
		files := byRec[id]
		if len(files) == 0 {
			return Planned{}, fmt.Errorf("recording: record %s has no file in the skeleton", id)
		}
		var paths []string
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		scope := append([]string(nil), pins[id].AffectedScope...)
		sort.Strings(paths)
		sort.Strings(scope)
		if strings.Join(paths, "\x00") != strings.Join(scope, "\x00") {
			return Planned{}, fmt.Errorf("recording: %s affected_scope %v must equal its planned files %v, or grow --check checks nothing", id, scope, paths)
		}
		rec := Record{RecordID: id, Gen: pins[id].GenHash, Files: []RFile{}}
		var invs []Invocation
		for _, f := range files {
			rf, err := fileEntry(f.Path)
			if err != nil {
				return Planned{}, err
			}
			rec.Files = append(rec.Files, rf)
			fi, declares, err := invocations(f)
			if err != nil {
				return Planned{}, err
			}
			invs = append(invs, fi...)
			if len(declares) > 0 {
				reg, err := region(f, pins[id], in.Tier, declares)
				if err != nil {
					return Planned{}, err
				}
				out.Regions = append(out.Regions, reg)
			}
		}
		rec.Phases = []Phase{{Name: "default", Invocations: invs}}
		out.Recording.Records = append(out.Recording.Records, rec)
	}
	sort.SliceStable(out.Regions, func(i, j int) bool { return out.Regions[i].Path < out.Regions[j].Path })
	return out, nil
}

func stemOf(p string) string { return strings.TrimSuffix(path.Base(p), ".go") }

// synthPath is the file that holds a skeleton file's convergence region.
func synthPath(p string) string { return path.Join(path.Dir(p), stemOf(p)+"_synth.go") }

// IsSynthPath reports whether p is a model-written synthesis file; everything
// else a render produces comes from sedum.
func IsSynthPath(p string) bool { return strings.HasSuffix(p, "_synth.go") }

func helperName(f File, s Stub) string { return stemOf(f.Path) + upperFirst(s.Name) + "Body" }

func packageName(f File) string {
	if f.Package != "" {
		return f.Package
	}
	dir := path.Dir(f.Path)
	if dir == "." || strings.HasPrefix(dir, "cmd/") {
		return "main"
	}
	return path.Base(dir)
}

// region builds the seeded synthesis region for f: package clause, the
// imports the helper signatures need, and one panicking helper per stub.
func region(f File, owner records.Pin, tier string, declares []string) (Region, error) {
	var b strings.Builder
	b.WriteString("package " + packageName(f) + "\n\n")
	all := strings.Join(declares, "\n")
	var imps []string
	for _, i := range f.Imports {
		if strings.Contains(all, path.Base(i)+".") {
			imps = append(imps, i)
		}
	}
	sort.Strings(imps)
	for _, i := range imps {
		b.WriteString("import " + strconv.Quote(i) + "\n")
	}
	b.WriteString("\n")
	for _, d := range declares {
		b.WriteString(d + " {\n\tpanic(\"graft: not converged\")\n}\n\n")
	}
	body, err := format.Source([]byte(b.String()))
	if err != nil {
		return Region{}, fmt.Errorf("recording: %s: seeded region does not format: %w", f.Path, err)
	}
	sp := synthPath(f.Path)
	// gofmt wants a blank line between the last declaration and the end marker.
	seed, err := converge.Seed("//", converge.Owner{Record: owner.RecordID, Version: owner.Version, Gen: owner.GenHash, Tier: tier}, string(body)+"\n")
	if err != nil {
		return Region{}, err
	}
	return Region{Path: sp, Owner: owner, Tier: tier, Declares: declares, Seed: seed}, nil
}

// validateSkeleton checks the whole skeleton before any record is read into
// it: paths, imports, that each decl's name and kind match its code, and that
// no name is declared twice in a package.
func validateSkeleton(sk Skeleton) error {
	paths := map[string]bool{}
	for _, f := range sk.Files {
		if paths[f.Path] {
			return fmt.Errorf("recording: file %s listed twice", f.Path)
		}
		paths[f.Path] = true
	}
	names := map[string]string{} // dir|recv|name -> file
	claim := func(f File, recv, name string) error {
		if name == "init" && recv == "" {
			return nil // any number of init functions is legal
		}
		k := path.Dir(f.Path) + "|" + recv + "|" + name
		if prev, dup := names[k]; dup {
			return fmt.Errorf("recording: %s declares %q, already declared in %s", f.Path, name, prev)
		}
		names[k] = f.Path
		return nil
	}
	for _, f := range sk.Files {
		if paths[synthPath(f.Path)] {
			return fmt.Errorf("recording: %s collides with the synth file of %s", synthPath(f.Path), f.Path)
		}
		seenImp := map[string]bool{}
		for _, i := range f.Imports {
			if i == "" || strings.ContainsAny(i, " \t\"") || seenImp[i] {
				return fmt.Errorf("recording: %s: bad or duplicate import %q", f.Path, i)
			}
			seenImp[i] = true
		}
		for _, d := range f.Decls {
			if err := checkDecl(f.Path, d); err != nil {
				return err
			}
			if err := claim(f, d.Receiver, d.Name); err != nil {
				return err
			}
		}
		for _, s := range f.Stubs {
			if s.Name == "" {
				return fmt.Errorf("recording: %s: stub without a name", f.Path)
			}
			if (s.ReceiverType == "") != (s.ReceiverName == "") {
				return fmt.Errorf("recording: %s: stub %s needs both receiver_name and receiver_type, or neither", f.Path, s.Name)
			}
			recv := strings.TrimPrefix(s.ReceiverType, "*")
			if err := claim(f, recv, s.Name); err != nil {
				return err
			}
			if err := claim(f, "", helperName(f, s)); err != nil {
				return err
			}
			for _, p := range s.Params {
				if p.Name == "" || p.Type == "" {
					return fmt.Errorf("recording: %s: stub %s has an unnamed parameter", f.Path, s.Name)
				}
			}
		}
	}
	return nil
}

// checkDecl parses d.Code and requires it to be exactly one declaration of
// the stated kind, name and receiver.
func checkDecl(file string, d Decl) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("recording: %s: decl %q: %s", file, d.Name, fmt.Sprintf(format, a...))
	}
	if d.Name == "" {
		return fmt.Errorf("recording: %s: decl without a name", file)
	}
	af, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+d.Code, parser.ParseComments)
	if err != nil {
		return bad("code does not parse: %v", err)
	}
	if len(af.Decls) != 1 {
		return bad("code holds %d declarations, want exactly one", len(af.Decls))
	}
	switch x := af.Decls[0].(type) {
	case *ast.FuncDecl:
		switch {
		case d.Kind == "func" && x.Recv == nil, d.Kind == "method" && x.Recv != nil:
		default:
			return bad("kind %q does not match the code", d.Kind)
		}
		if x.Name.Name != d.Name {
			return bad("code declares %q", x.Name.Name)
		}
		if d.Kind == "func" && d.Receiver != "" {
			return bad("a func has no receiver")
		}
		if d.Kind == "method" && recvName(x.Recv) != d.Receiver {
			return bad("receiver is %q in the code, %q in the decl", recvName(x.Recv), d.Receiver)
		}
	case *ast.GenDecl:
		want := map[string]token.Token{"type": token.TYPE, "var": token.VAR, "const": token.CONST}[d.Kind]
		if want == token.ILLEGAL || x.Tok != want {
			return bad("kind %q does not match the code", d.Kind)
		}
		if len(x.Specs) != 1 {
			return bad("grouped declarations are not allowed")
		}
		ok := false
		switch sp := x.Specs[0].(type) {
		case *ast.TypeSpec:
			ok = sp.Name.Name == d.Name
		case *ast.ValueSpec:
			ok = len(sp.Names) == 1 && sp.Names[0].Name == d.Name
		}
		if !ok {
			return bad("code does not declare exactly %q", d.Name)
		}
	default:
		return bad("unsupported declaration")
	}
	if d.Kind != "method" && d.Receiver != "" {
		return bad("only methods have a receiver")
	}
	return nil
}

func recvName(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) != 1 {
		return ""
	}
	t := fl.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func fileEntry(p string) (RFile, error) {
	if !strings.HasSuffix(p, ".go") || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return RFile{}, fmt.Errorf("recording: bad file path %q", p)
	}
	name := strings.TrimSuffix(path.Base(p), ".go")
	if strings.HasSuffix(name, "_synth") {
		return RFile{}, fmt.Errorf("recording: %s: _synth.go files belong to convergence regions", p)
	}
	dir := path.Dir(p)
	switch {
	case dir == ".":
		return RFile{Path: p, Package: "go", Template: "{name}.go", Captures: map[string]string{"name": name}}, nil
	case strings.HasPrefix(dir, "cmd/") && strings.Count(dir, "/") == 1:
		return RFile{Path: p, Package: "go", Template: "cmd/{bin}/{name}.go",
			Captures: map[string]string{"bin": strings.TrimPrefix(dir, "cmd/"), "name": name}}, nil
	case !strings.Contains(dir, "/"):
		return RFile{Path: p, Package: "go", Template: "{pkg}/{name}.go", Captures: map[string]string{"pkg": dir, "name": name}}, nil
	}
	return RFile{}, fmt.Errorf("recording: unsupported file layout %q", p)
}

func invocations(f File) ([]Invocation, []string, error) {
	var invs []Invocation
	imps := append([]string(nil), f.Imports...)
	sort.Strings(imps)
	for _, i := range imps {
		invs = append(invs, Invocation{"addImport", map[string]string{"file": f.Path, "path": i}})
	}
	for _, d := range f.Decls {
		switch d.Kind {
		case "type", "var", "const", "func":
			act := "set" + upperFirst(d.Kind)
			if d.Kind == "func" {
				act = "setFunction"
			}
			invs = append(invs, Invocation{act,
				map[string]string{"file": f.Path, "name": d.Name, "code": d.Code}})
		case "method":
			invs = append(invs, Invocation{"setMethod",
				map[string]string{"file": f.Path, "receiver": d.Receiver, "name": d.Name, "code": d.Code}})
		default:
			return nil, nil, fmt.Errorf("recording: %s: unknown decl kind %q", f.Path, d.Kind)
		}
	}
	var declares []string
	for _, s := range f.Stubs {
		helper := helperName(f, s)
		params, args := paramText(s.Params)
		res := resultText(s.Results)
		call := helper + "(" + args + ")"
		sig := "func "
		stubSig := "func "
		if s.ReceiverType != "" {
			recv := "(" + s.ReceiverName + " " + s.ReceiverType + ") "
			stubSig += recv
			sig += recv
			call = s.ReceiverName + "." + call
		}
		stubSig += s.Name + "(" + params + ")" + res
		sig += helper + "(" + params + ")" + res
		body := call
		if len(s.Results) > 0 {
			body = "return " + call
		}
		doc := "// " + s.Name + " is rendered by graft; its body is a convergence region (" + helper + ")."
		if s.Doc != "" {
			doc = "// " + strings.ReplaceAll(strings.TrimSpace(s.Doc), "\n", "\n// ") + "\n" + doc
		}
		code := doc + "\n" + stubSig + " {\n\t" + body + "\n}"
		kw := map[string]string{"file": f.Path, "name": s.Name, "code": code}
		act := "setFunction"
		if s.ReceiverType != "" {
			act = "setMethod"
			kw["receiver"] = strings.TrimPrefix(s.ReceiverType, "*")
		}
		invs = append(invs, Invocation{act, kw})
		declares = append(declares, sig)
	}
	return invs, declares, nil
}

func paramText(ps []Param) (decl, args string) {
	var d, a []string
	for _, p := range ps {
		d = append(d, p.Name+" "+p.Type)
		if strings.HasPrefix(p.Type, "...") {
			a = append(a, p.Name+"...")
		} else {
			a = append(a, p.Name)
		}
	}
	return strings.Join(d, ", "), strings.Join(a, ", ")
}

func resultText(rs []string) string {
	switch len(rs) {
	case 0:
		return ""
	case 1:
		return " " + rs[0]
	}
	return " (" + strings.Join(rs, ", ") + ")"
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// Write emits sedum's on-disk recording JSON.
func Write(w io.Writer, r Recording) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// ErrEmpty is returned by Load for a skeleton with no files.
var ErrEmpty = errors.New("recording: skeleton has no files")

// LoadSkeleton parses skeleton JSON.
func LoadSkeleton(b []byte) (Skeleton, error) {
	var s Skeleton
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Skeleton{}, fmt.Errorf("recording: skeleton: %w", err)
	}
	if len(s.Files) == 0 {
		return Skeleton{}, ErrEmpty
	}
	return s, nil
}
