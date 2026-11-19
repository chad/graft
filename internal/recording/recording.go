// Package recording is graft's planning layer (SPEC §2, §5): it turns accepted
// records plus a typed skeleton into a sedum recording, deterministically and
// with no model call. Sedum is invoked to render it (internal/render), never
// imported.
//
// What a record cannot express through catalog actions is not invented here.
// A function whose body is open is rendered as a stub that only delegates to a
// helper. The helpers live in a separate file, <stem>_synth<ext>, owned entirely
// by one convergence region (SPEC §6): the region's body is the whole file
// (package clause, imports, helpers), so converge can add imports and sedum's
// file templates never see the helpers. Plan seeds each region with helpers
// that fail with "graft: not converged", so the package builds before converge
// runs and each region can be gated on its own.
//
// Plan knows no language. How a stub, a helper signature and a seed are
// written, what a synth file is called and what the files' extension is come
// from the generator package's convergence contract (sedum convergence
// render), reached through the Contract interface.
package recording

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chad/graft/internal/contract"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/records"
)

// SedumVersion is written into every recording.
const SedumVersion = "0.12.2"

// Decl is one fixed top-level declaration (type, var, const, or a function
// whose whole text is part of the skeleton). Code is in the generator's
// language, written as the file should contain it.
type Decl struct {
	Kind     string `json:"kind"` // type | var | const | func | method
	Name     string `json:"name"`
	Receiver string `json:"receiver,omitempty"` // method only: type name, no star
	Code     string `json:"code"`
}

// Param is a named parameter.
type Param = contract.Param

// Stub is a function or method whose body is left to convergence.
type Stub = contract.Stub

// File is one source file owned by one record.
type File struct {
	Path string `json:"path"`
	// Record names the owning record by id; Section names it by the spec
	// section the record was derived from. Exactly one is set.
	Record  string `json:"record,omitempty"`
	Section string `json:"section,omitempty"`
	// Package overrides the synth file's package name where the language has
	// one (the generator's convergence block says how it is derived otherwise).
	Package string   `json:"package,omitempty"`
	Imports []string `json:"imports,omitempty"`
	Decls   []Decl   `json:"decls,omitempty"`
	Stubs   []Stub   `json:"stubs,omitempty"`
}

// Skeleton is the typed layout input. Records carry only statements, so the
// declarations come from here.
type Skeleton struct {
	// Generator names the sedum generator package that renders this tree. It
	// defaults to Input.Generator (the language profile's name).
	Generator string `json:"generator,omitempty"`
	Files     []File `json:"files"`
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
	// Contract reads the generator's convergence block and renders stubs and
	// seeds from it. Required.
	Contract contract.Contract
	// Generator is the default generator package name ("go" when empty).
	Generator string
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
	// SynthSuffix is the generator's synth-file stem suffix (info.SynthSuffix).
	SynthSuffix string
}

// Planned is Plan's result.
type Planned struct {
	Recording Recording
	Regions   []Region
	// SynthSuffix is the generator's synth-file stem suffix.
	SynthSuffix string
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

	if in.Contract == nil {
		return Planned{}, errors.New("recording: Input.Contract is required (the generator's convergence contract)")
	}
	gen := in.Skeleton.Generator
	if gen == "" {
		gen = in.Generator
	}
	if gen == "" {
		gen = "go"
	}
	info, err := in.Contract.Info()
	if err != nil {
		return Planned{}, fmt.Errorf("recording: %w", err)
	}
	if info.Placeholder != Placeholder {
		return Planned{}, fmt.Errorf("recording: generator %s uses placeholder %q, want %q", gen, info.Placeholder, Placeholder)
	}
	if len(info.Extensions) == 0 {
		return Planned{}, fmt.Errorf("recording: generator %s declares no extension", gen)
	}
	rendered, err := renderContract(in, info)
	if err != nil {
		return Planned{}, err
	}

	out := Planned{SynthSuffix: info.SynthSuffix, Recording: Recording{
		SedumVersion: SedumVersion,
		Complete:     true,
		Packages:     map[string]Package{gen: {Extensions: info.Extensions}},
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
			rf, err := fileEntry(f.Path, gen, info)
			if err != nil {
				return Planned{}, err
			}
			rec.Files = append(rec.Files, rf)
			of := rendered[f.Path]
			fi, declares, err := invocations(f, of)
			if err != nil {
				return Planned{}, err
			}
			invs = append(invs, fi...)
			if len(declares) > 0 {
				seed, err := converge.Seed(info.CommentPrefix, converge.Owner{Record: pins[id].RecordID, Version: pins[id].Version, Gen: pins[id].GenHash, Tier: in.Tier}, of.Seed)
				if err != nil {
					return Planned{}, err
				}
				out.Regions = append(out.Regions, Region{Path: of.SynthPath, Owner: pins[id], Tier: in.Tier, Declares: declares, Seed: seed, SynthSuffix: info.SynthSuffix})
			}
		}
		rec.Phases = []Phase{{Name: "default", Invocations: invs}}
		out.Recording.Records = append(out.Recording.Records, rec)
	}
	sort.SliceStable(out.Regions, func(i, j int) bool { return out.Regions[i].Path < out.Regions[j].Path })
	return out, nil
}

// renderContract asks the contract about every skeleton file at once and
// checks what it answered, without parsing any code: each helper name appears
// in its seed and in its stub, names are claimed once per directory, and no
// synth file collides with a skeleton file.
func renderContract(in Input, info contract.Info) (map[string]contract.RenderedFile, error) {
	var cin contract.Input
	for _, f := range in.Skeleton.Files {
		cin.Files = append(cin.Files, contract.File{Path: f.Path, Package: f.Package, Imports: f.Imports, Stubs: f.Stubs})
	}
	cout, err := in.Contract.Render(cin)
	if err != nil {
		return nil, fmt.Errorf("recording: convergence render: %w", err)
	}
	if len(cout.Files) != len(in.Skeleton.Files) {
		return nil, fmt.Errorf("recording: convergence render returned %d files for %d", len(cout.Files), len(in.Skeleton.Files))
	}
	byPath := map[string]contract.RenderedFile{}
	paths := map[string]bool{}
	for _, f := range in.Skeleton.Files {
		paths[f.Path] = true
	}
	names := map[string]string{} // dir|recv|name -> file
	claim := func(file, recv, name string) error {
		if name == "init" && recv == "" {
			return nil // any number of init functions is legal
		}
		k := path.Dir(file) + "|" + recv + "|" + name
		if prev, dup := names[k]; dup {
			return fmt.Errorf("recording: %s declares %q, already declared in %s", file, name, prev)
		}
		names[k] = file
		return nil
	}
	for i, f := range in.Skeleton.Files {
		of := cout.Files[i]
		if of.Path != f.Path {
			return nil, fmt.Errorf("recording: convergence render answered %s for %s", of.Path, f.Path)
		}
		if paths[of.SynthPath] {
			return nil, fmt.Errorf("recording: %s collides with the synth file of %s", of.SynthPath, f.Path)
		}
		if len(of.Stubs) != len(f.Stubs) {
			return nil, fmt.Errorf("recording: %s: convergence render returned %d stubs for %d", f.Path, len(of.Stubs), len(f.Stubs))
		}
		for _, d := range f.Decls {
			if err := claim(f.Path, d.Receiver, d.Name); err != nil {
				return nil, err
			}
		}
		for _, s := range of.Stubs {
			if !strings.Contains(of.Seed, s.Helper) || !strings.Contains(s.Stub, s.Helper) || !strings.Contains(s.Declare, s.Helper) {
				return nil, fmt.Errorf("recording: %s: helper %q of stub %s must appear in its stub, signature and seed", f.Path, s.Helper, s.Name)
			}
			if !strings.Contains(of.Seed, Placeholder) {
				return nil, fmt.Errorf("recording: %s: the seed lacks the placeholder %q", f.Path, Placeholder)
			}
			if err := claim(f.Path, s.Kwargs["receiver"], s.Kwargs["name"]); err != nil {
				return nil, err
			}
			if err := claim(f.Path, "", s.Helper); err != nil {
				return nil, err
			}
		}
		byPath[f.Path] = of
	}
	if info.SynthSuffix != cout.SynthSuffix {
		return nil, fmt.Errorf("recording: convergence info says suffix %q, render says %q", info.SynthSuffix, cout.SynthSuffix)
	}
	return byPath, nil
}

// Placeholder is the message of a seeded, not-yet-converged helper. A body
// that still contains it is never converged. It is fixed by sedum's core.
const Placeholder = "graft: not converged"

// IsSynthPath reports whether p is a model-written synthesis file (<stem> plus
// the generator's synth suffix plus the extension); everything else a render
// produces comes from sedum. suffix is the generator's (Planned.SynthSuffix).
func IsSynthPath(p, suffix string) bool {
	return suffix != "" && strings.HasSuffix(strings.TrimSuffix(p, path.Ext(p)), suffix)
}

// containsIdent reports whether code contains name as a whole identifier: not
// as part of a longer one. It reads no syntax, so it holds for any language.
func containsIdent(code, name string) bool {
	isID := func(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	for i := 0; ; {
		j := strings.Index(code[i:], name)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(name)
		before, _ := utf8.DecodeLastRuneInString(code[:a])
		after, _ := utf8.DecodeRuneInString(code[b:])
		if (a == 0 || !isID(before)) && (b == len(code) || !isID(after)) {
			return true
		}
		i = a + 1
	}
}

// validateSkeleton checks the layout before any record is read into it: paths
// and imports, and that no declaration name is claimed twice in a directory.
// Stub and helper names are checked after the contract names them.
func validateSkeleton(sk Skeleton) error {
	paths := map[string]bool{}
	for _, f := range sk.Files {
		if paths[f.Path] {
			return fmt.Errorf("recording: file %s listed twice", f.Path)
		}
		paths[f.Path] = true
	}
	for _, f := range sk.Files {
		seenImp := map[string]bool{}
		for _, i := range f.Imports {
			if i == "" || strings.ContainsAny(i, " \t\"") || seenImp[i] {
				return fmt.Errorf("recording: %s: bad or duplicate import %q", f.Path, i)
			}
			seenImp[i] = true
		}
		for _, d := range f.Decls {
			if d.Name == "" {
				return fmt.Errorf("recording: %s: decl without a name", f.Path)
			}
			if d.Kind != "method" && d.Receiver != "" {
				return fmt.Errorf("recording: %s: decl %q: only methods have a receiver", f.Path, d.Name)
			}
			if !containsIdent(d.Code, d.Name) {
				return fmt.Errorf("recording: %s: decl %q: its code does not contain that name as a whole identifier", f.Path, d.Name)
			}
		}
		for _, s := range f.Stubs {
			if s.Name == "" {
				return fmt.Errorf("recording: %s: stub without a name", f.Path)
			}
			if (s.ReceiverType == "") != (s.ReceiverName == "") {
				return fmt.Errorf("recording: %s: stub %s needs both receiver_name and receiver_type, or neither", f.Path, s.Name)
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

// fileEntry maps a skeleton path to the file template of the generator
// package: {name}<ext> at the root, {pkg}/{name}<ext> one directory down, and
// cmd/{bin}/{name}<ext> for a binary directory.
func fileEntry(p, gen string, info contract.Info) (RFile, error) {
	ext := ""
	for _, e := range info.Extensions {
		if strings.HasSuffix(p, e) {
			ext = e
			break
		}
	}
	if ext == "" || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return RFile{}, fmt.Errorf("recording: bad file path %q (generator %s handles %s)", p, gen, strings.Join(info.Extensions, ", "))
	}
	name := strings.TrimSuffix(path.Base(p), ext)
	if strings.HasSuffix(name, info.SynthSuffix) {
		return RFile{}, fmt.Errorf("recording: %s: %s files belong to convergence regions", p, info.SynthSuffix+ext)
	}
	dir := path.Dir(p)
	switch {
	case dir == ".":
		return RFile{Path: p, Package: gen, Template: "{name}" + ext, Captures: map[string]string{"name": name}}, nil
	case strings.HasPrefix(dir, "cmd/") && strings.Count(dir, "/") == 1:
		return RFile{Path: p, Package: gen, Template: "cmd/{bin}/{name}" + ext,
			Captures: map[string]string{"bin": strings.TrimPrefix(dir, "cmd/"), "name": name}}, nil
	case !strings.Contains(dir, "/"):
		return RFile{Path: p, Package: gen, Template: "{pkg}/{name}" + ext, Captures: map[string]string{"pkg": dir, "name": name}}, nil
	}
	return RFile{}, fmt.Errorf("recording: unsupported file layout %q", p)
}

// invocations lists the catalog calls for one file: its imports (the contract
// adds the one a stub file needs for its helpers), its declarations, then its
// stubs. declares are the helper signatures the model must implement.
func invocations(f File, of contract.RenderedFile) ([]Invocation, []string, error) {
	var invs []Invocation
	for _, kw := range of.Imports {
		invs = append(invs, Invocation{"addImport", kw})
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
	for _, s := range of.Stubs {
		invs = append(invs, Invocation{s.Action, s.Kwargs})
		declares = append(declares, s.Declare)
	}
	return invs, declares, nil
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
