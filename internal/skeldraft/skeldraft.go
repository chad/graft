// Package skeldraft proposes a skeleton: the shape of every file a spec's
// sections own (fixed decls and stubs), for `graft draft --skeleton`.
//
// It proposes and never applies. The proposal is the skeleton JSON that
// `graft run --render --skeleton` already imports, so acceptance is that
// existing import (SPEC: the shape lands in the owning imprint, with a version
// and a receipt). Nothing here writes a record or touches the real tree.
//
// Before a proposal is offered it passes, cheapest first:
//  1. a strict parse and structural checks (known sections, decl kinds);
//  2. the profile's decl_check, which rejects logic in a drafted decl (graft
//     parses no target code: the language's own checker does);
//  3. a sedum render into a scratch tree, with placeholders seeded, and the
//     profile's build gate on that tree.
//
// A failure goes back to the model, with the checker's and the build's own
// words, for a bounded number of retries; then it fails loudly.
package skeldraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/recording"
)

// DefaultRetries is how many times an invalid proposal goes back to the model.
const DefaultRetries = 2

// MaxRetries caps --retries.
const MaxRetries = 5

var (
	// ErrInvalid: no valid proposal within the retries.
	ErrInvalid = errors.New("skeldraft: no valid skeleton proposed")
	// ErrModel: the model command failed. Infrastructure, not a verdict.
	ErrModel = errors.New("skeldraft: model command failed")
)

// DeclIn is one drafted decl, as the decl_check reads it on stdin.
type DeclIn struct {
	File string `json:"file"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// Config is one drafting run.
type Config struct {
	Model       model.Model
	Profile     lang.Profile
	Spec        string
	Conventions string
	Retries     int  // 0 means DefaultRetries
	Strict      bool // an unowned statement counts as invalid and is retried

	// FileFor is the one file a section owns (a record's affected_scope is
	// exactly one file under --render). Required.
	FileFor func(section string) string
	// Check runs the profile's decl_check. It returns the checker's complaints
	// (empty: accepted). Required.
	Check func(ctx context.Context, decls []DeclIn) (string, error)
	// Scratch renders sk with sedum into a scratch tree, seeds it, and returns
	// the tree and a cleanup. Required.
	Scratch func(ctx context.Context, drafts []drafter.Draft, sk recording.Skeleton) (string, func(), error)
	// Build runs the profile's build gate in the scratch tree and returns its
	// output when it fails (empty: it passed). Required.
	Build func(ctx context.Context, root string) (string, error)
}

// Statement is one spec statement, numbered for the prompt and the report.
type Statement struct {
	ID      string // S1, S2, ...
	Section string
	Type    drafter.Type
	Text    string
}

// Owner is one line of the coverage report.
type Owner struct {
	Statement Statement
	Claimed   []string // "path#name" the model says own it and that exist
	Dangling  []string // claimed refs that name nothing in the proposal
}

// Unowned reports whether no existing stub or decl is claimed for it.
func (o Owner) Unowned() bool { return len(o.Claimed) == 0 }

// Result is a valid proposal.
type Result struct {
	Skeleton recording.Skeleton
	Coverage []Owner
	Calls    int // model calls made
}

// Unowned lists the statements no stub owns: the report's hard signal.
func (r Result) Unowned() []Owner {
	var out []Owner
	for _, o := range r.Coverage {
		if o.Unowned() {
			out = append(out, o)
		}
	}
	return out
}

// Statements numbers every drafted statement of the spec, in order.
func Statements(drafts []drafter.Draft) []Statement {
	var out []Statement
	for _, d := range drafts {
		for _, s := range d.Statements {
			out = append(out, Statement{ID: fmt.Sprintf("S%d", len(out)+1), Section: d.Section, Type: s.Type, Text: s.Text})
		}
	}
	return out
}

// Draft asks the model for a skeleton and validates it. It returns the first
// proposal that passes, or ErrInvalid (wrapping the last failure) once the
// retries are spent. The number of calls made is in the Result either way
// (a failed run returns it in the Result too).
func Draft(ctx context.Context, cfg Config) (Result, error) {
	var res Result
	if cfg.Model == nil || cfg.FileFor == nil || cfg.Check == nil || cfg.Scratch == nil || cfg.Build == nil {
		return res, errors.New("skeldraft: Model, FileFor, Check, Scratch and Build are required")
	}
	if len(cfg.Profile.DeclCheck) == 0 {
		return res, fmt.Errorf("skeldraft: the %s language profile has no decl_check, so it cannot draft a skeleton (a drafted decl must be proven free of logic)", cfg.Profile.Name)
	}
	retries := cfg.Retries
	if retries == 0 {
		retries = DefaultRetries
	}
	if retries < 0 || retries > MaxRetries {
		return res, fmt.Errorf("skeldraft: retries must be 0..%d", MaxRetries)
	}
	drafts := drafter.Drafts(cfg.Spec)
	stmts := Statements(drafts)
	if len(stmts) == 0 {
		return res, errors.New("skeldraft: the spec has no statements to draft a skeleton for")
	}
	feedback := ""
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		ans, err := cfg.Model.Complete(ctx, prompt(cfg, drafts, stmts, feedback))
		res.Calls++
		if err != nil {
			return res, fmt.Errorf("%w: %v", ErrModel, err)
		}
		sk, owners, err := validate(ctx, cfg, drafts, stmts, ans)
		if err == nil {
			res.Skeleton = sk
			res.Coverage = coverage(stmts, sk, owners)
			if cfg.Strict && len(res.Unowned()) > 0 {
				err = fmt.Errorf("%d statement(s) have no owning stub or decl (--strict): %s", len(res.Unowned()), ids(res.Unowned()))
			} else {
				return res, nil
			}
		}
		last = err
		feedback = err.Error()
	}
	res.Skeleton, res.Coverage = recording.Skeleton{}, nil
	return res, fmt.Errorf("%w after %d call(s): %v", ErrInvalid, res.Calls, last)
}

func ids(os []Owner) string {
	var s []string
	for _, o := range os {
		s = append(s, o.Statement.ID)
	}
	return strings.Join(s, ", ")
}

// answer is the model's reply: the skeleton and its claim of who owns what.
type answer struct {
	Files  []recording.File    `json:"files"`
	Owners map[string][]string `json:"owners"`
}

// validate runs the checks, cheapest first. Its error text is what the model
// sees on a retry.
func validate(ctx context.Context, cfg Config, drafts []drafter.Draft, stmts []Statement, resp string) (recording.Skeleton, map[string][]string, error) {
	var zero recording.Skeleton
	a, err := parse(resp)
	if err != nil {
		return zero, nil, err
	}
	if err := structure(a, drafts, stmts, cfg.FileFor); err != nil {
		return zero, nil, err
	}
	var decls []DeclIn
	for _, f := range a.Files {
		for _, d := range f.Decls {
			decls = append(decls, DeclIn{File: f.Path, Kind: d.Kind, Name: d.Name, Code: d.Code})
		}
	}
	if len(decls) > 0 {
		out, err := cfg.Check(ctx, decls)
		if err != nil {
			return zero, nil, fmt.Errorf("decl_check could not run: %w", err)
		}
		if out != "" {
			return zero, nil, fmt.Errorf("decl_check rejected the drafted decls (decls are types, consts and signatures only; no logic):\n%s", strings.TrimSpace(out))
		}
	}
	sk := recording.Skeleton{Files: a.Files}
	root, cleanup, err := cfg.Scratch(ctx, drafts, sk)
	if err != nil {
		return zero, nil, fmt.Errorf("rendering the proposal with sedum failed: %w", err)
	}
	defer cleanup()
	out, err := cfg.Build(ctx, root)
	if err != nil {
		return zero, nil, fmt.Errorf("the build gate could not run: %w", err)
	}
	if out != "" {
		return zero, nil, fmt.Errorf("the rendered, seeded tree does not build (%s):\n%s", strings.Join(lang.ExpandArgv(cfg.Profile.Build, map[string]string{"out": "<bin>"}), " "), strings.TrimSpace(out))
	}
	return sk, a.Owners, nil
}

func parse(resp string) (answer, error) {
	var a answer
	body := model.ExtractCode(resp, "json")
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("the answer is not the required JSON object {\"files\": [...], \"owners\": {...}} in one ```json block: %v", err)
	}
	if dec.More() {
		return a, errors.New("trailing data after the JSON object")
	}
	return a, nil
}

// structure is the cheap, no-build, no-subprocess part of validation.
func structure(a answer, drafts []drafter.Draft, stmts []Statement, fileFor func(string) string) error {
	sections := map[string]bool{}
	for _, d := range drafts {
		sections[d.Section] = true
	}
	var bad []string
	if len(a.Files) == 0 {
		bad = append(bad, "files is empty")
	}
	paths := map[string]bool{}
	owned := map[string]bool{}
	for _, f := range a.Files {
		switch {
		case f.Path == "":
			bad = append(bad, "a file has no path")
		case paths[f.Path]:
			bad = append(bad, fmt.Sprintf("file %s is listed twice", f.Path))
		}
		paths[f.Path] = true
		if f.Record != "" {
			bad = append(bad, fmt.Sprintf("%s: use \"section\", never \"record\" (no records exist yet)", f.Path))
		}
		if !sections[f.Section] {
			bad = append(bad, fmt.Sprintf("%s: section %q is not a section of the spec", f.Path, f.Section))
		}
		if sections[f.Section] {
			if want := fileFor(f.Section); f.Path != want {
				bad = append(bad, fmt.Sprintf("%s: section %q must own exactly the file %q (one file per section, at that path)", f.Path, f.Section, want))
			}
			if owned[f.Section] {
				bad = append(bad, fmt.Sprintf("section %q has more than one file; a section owns exactly one", f.Section))
			}
		}
		owned[f.Section] = true
		for _, d := range f.Decls {
			switch d.Kind {
			case "type", "const", "var":
			default:
				bad = append(bad, fmt.Sprintf("%s: decl %q has kind %q; drafted decls are type, const or var only, and a function or method is a stub (bodies are never drafted)", f.Path, d.Name, d.Kind))
			}
		}
		for _, s := range f.Stubs {
			if strings.TrimSpace(s.Doc) == "" {
				bad = append(bad, fmt.Sprintf("%s: stub %s has no doc contract", f.Path, s.Name))
			}
		}
	}
	for _, d := range drafts {
		if !owned[d.Section] {
			bad = append(bad, fmt.Sprintf("section %q owns no file (it must own %q)", d.Section, fileFor(d.Section)))
		}
	}
	known := map[string]bool{}
	for _, s := range stmts {
		known[s.ID] = true
	}
	for id := range a.Owners {
		if !known[id] {
			bad = append(bad, fmt.Sprintf("owners names %q, which is not a statement id", id))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return errors.New("the proposal is malformed:\n- " + strings.Join(bad, "\n- "))
	}
	return nil
}

// coverage maps each statement to the stubs and decls the model CLAIMS own it.
// A claim naming nothing in the proposal is dangling and does not count.
func coverage(stmts []Statement, sk recording.Skeleton, owners map[string][]string) []Owner {
	exists := map[string]bool{}
	for _, f := range sk.Files {
		for _, s := range f.Stubs {
			exists[f.Path+"#"+s.Name] = true
		}
		for _, d := range f.Decls {
			exists[f.Path+"#"+d.Name] = true
		}
	}
	var out []Owner
	for _, st := range stmts {
		o := Owner{Statement: st}
		for _, ref := range owners[st.ID] {
			if exists[ref] {
				o.Claimed = append(o.Claimed, ref)
			} else {
				o.Dangling = append(o.Dangling, ref)
			}
		}
		out = append(out, o)
	}
	return out
}
