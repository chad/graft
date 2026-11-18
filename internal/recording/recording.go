// Package recording is graft's planning layer (SPEC §2, §5): it turns accepted
// records plus a typed skeleton into a sedum recording, deterministically and
// with no model call. Sedum is invoked to render it (internal/render), never
// imported.
//
// What a record cannot express through catalog actions is not invented here.
// A function whose body is open is rendered as a stub that only delegates to a
// helper, and the helper's signature is declared in a convergence region
// (SPEC §6) that `graft converge` fills. A sedum region holds a whole
// declaration and converge refuses a synthesis region inside one, so the body
// can never live inside the rendered function itself.
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
}

// File is one source file owned by one record.
type File struct {
	Path    string   `json:"path"`
	Record  string   `json:"record"`
	Imports []string `json:"imports,omitempty"`
	Decls   []Decl   `json:"decls,omitempty"`
	Stubs   []Stub   `json:"stubs,omitempty"`
}

// Skeleton is the typed layout input. Records carry only statements, so the
// declarations come from here.
type Skeleton struct {
	Files []File `json:"files"`
}

// Input is everything Plan reads.
type Input struct {
	Pins     []records.Pin
	Skeleton Skeleton
}

// Recording is sedum's on-disk recording.
type Recording struct {
	SedumVersion string             `json:"sedum_version"`
	Packages     map[string]Package `json:"packages"`
	Records      []Record           `json:"records"`
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

// Region is a convergence region a record owns: converge fills it and must
// declare every function in Declares, because the rendered stubs call them.
type Region struct {
	Path     string
	Owner    records.Pin
	Declares []string // full signatures, no body
}

// Planned is Plan's result.
type Planned struct {
	Recording Recording
	Regions   []Region
}

// Plan is deterministic: the same input gives a byte-identical recording.
func Plan(in Input) (Planned, error) {
	pins := map[string]records.Pin{}
	for _, p := range in.Pins {
		if _, dup := pins[p.RecordID]; dup {
			return Planned{}, fmt.Errorf("recording: duplicate record %q", p.RecordID)
		}
		pins[p.RecordID] = p
	}
	byRec := map[string][]File{}
	seen := map[string]bool{}
	for _, f := range in.Skeleton.Files {
		if _, ok := pins[f.Record]; !ok {
			return Planned{}, fmt.Errorf("recording: file %s names unknown record %q", f.Path, f.Record)
		}
		if seen[f.Path] {
			return Planned{}, fmt.Errorf("recording: file %s listed twice", f.Path)
		}
		seen[f.Path] = true
		byRec[f.Record] = append(byRec[f.Record], f)
	}
	ids := make([]string, 0, len(byRec))
	for id := range byRec {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := Planned{Recording: Recording{
		SedumVersion: SedumVersion,
		Packages:     map[string]Package{"go": {Extensions: []string{".go"}}},
		Records:      []Record{},
	}}
	for _, id := range ids {
		rec := Record{RecordID: id, Gen: pins[id].GenHash, Files: []RFile{}}
		var invs []Invocation
		for _, f := range byRec[id] {
			rf, err := fileEntry(f.Path)
			if err != nil {
				return Planned{}, err
			}
			rec.Files = append(rec.Files, rf)
			fi, region, err := invocations(f)
			if err != nil {
				return Planned{}, err
			}
			invs = append(invs, fi...)
			if len(region) > 0 {
				out.Regions = append(out.Regions, Region{Path: f.Path, Owner: pins[id], Declares: region})
			}
		}
		rec.Phases = []Phase{{Name: "default", Invocations: invs}}
		out.Recording.Records = append(out.Recording.Records, rec)
	}
	return out, nil
}

func fileEntry(p string) (RFile, error) {
	if !strings.HasSuffix(p, ".go") || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return RFile{}, fmt.Errorf("recording: bad file path %q", p)
	}
	name := strings.TrimSuffix(path.Base(p), ".go")
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
	stem := strings.TrimSuffix(path.Base(f.Path), ".go")
	var declares []string
	for _, s := range f.Stubs {
		if s.Name == "" {
			return nil, nil, fmt.Errorf("recording: %s: stub without a name", f.Path)
		}
		helper := stem + upperFirst(s.Name) + "Body"
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
		code := "// " + s.Name + " is rendered by graft; its body is a convergence region\n// (" + helper + ").\n" +
			stubSig + " {\n\t" + body + "\n}"
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
