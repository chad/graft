package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chad/graft/internal/contract"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// ErrModel: the model command failed (timeout, CLI error, empty answer). That
// is infrastructure, not a verdict on the code.
var ErrModel = errors.New("pipeline: model command failed")

// Config is one `graft run`.
type Config struct {
	Repo   string // provenance repo (records + .graft index)
	Root   string // the code tree being generated
	Module string // module name for a fresh tree ({module} in the profile's bootstrap)
	// Lang is the target language; the zero value is lang.Go.
	Lang        lang.Profile
	Spec        string // the full spec text, shown to the model as context
	Conventions string // architecture conventions, shown to the model
	// Visible are checks the model is shown (name → source), e.g. a smoke
	// suite that also runs as a gate. Never the held-out oracle.
	Visible    map[string]string
	Tier       string
	Attempts   int // per record; 0 means 3
	Model      model.Model
	Gates      []converge.Gate
	Policy     evidence.Policy
	Log        *evidence.Log
	Progress   io.Writer           // nil for silence
	Skeleton   *recording.Skeleton // non-nil (with Sedum): sedum lays out the tree, the model writes only Body helpers
	Sedum      render.Sedum
	Generators string // sedum's generators directory (contains the generator package); needed with Skeleton
	// Contract overrides the generator's convergence contract (tests). By
	// default it is `sedum convergence` run with Sedum's binary against
	// Generators, for the skeleton's generator package (the profile's name when
	// the skeleton names none).
	Contract contract.Contract
	Samples  int // model answers drawn per attempt; 0 or 1 means one (today's behaviour)

	skelHash string // render mode: the skeleton hash pinned into every evidence entry
}

// Outcome is what happened to one record.
type Outcome struct {
	ID, Section, File string
	Applied           bool
	Attempts          int
	LastFailure       string
	Samples           int // answers drawn per attempt (1 unless best-of-N)
	Calls             int // model calls actually made for this record
	// Reason says why the region was looked at again, in render mode: "" when
	// it was up to date, "skeleton_hash" when only the skeleton moved (the body
	// was re-verified under the gates with no model call), "gen_hash" when the
	// record's own content (its shape included) moved or its version did, and
	// "new" when the region had no body yet.
	Reason string
	// Regenerated is true when the existing body failed its re-check and the
	// model was asked for a new one.
	Regenerated bool
}

// Run converges one region per accepted record, in spec order. A record whose
// region already holds an unedited body written for its current pin is not
// regenerated, but it is re-verified: its existing body is run through the
// CURRENT gates and policy (no model call), so a changed gate cannot be
// skipped. Only if that re-check fails is the region regenerated. Changing the
// model or the conventions does not, by itself, regenerate anything: intent
// did not change.
func Run(ctx context.Context, cfg Config, acc []Accepted) ([]Outcome, error) {
	if cfg.Attempts == 0 {
		cfg.Attempts = 3
	}
	if cfg.Samples < 1 {
		cfg.Samples = 1
	}
	if err := bootstrap(cfg); err != nil {
		return nil, err
	}
	store, err := records.OpenDir(cfg.Repo)
	if err != nil {
		return nil, err
	}
	sort.Slice(acc, func(i, j int) bool { return acc[i].Entry.Order < acc[j].Entry.Order })
	var byRec map[string]recording.Region
	var pending map[string]bool // regions still holding their seeded placeholder
	if cfg.Skeleton != nil {
		if byRec, pending, cfg.skelHash, err = cfg.renderTree(ctx, store, acc); err != nil {
			return nil, err
		}
	}
	c := converge.Converger{Root: cfg.Root, Records: store, Log: cfg.Log, Policy: cfg.Policy, Gates: cfg.Gates, By: "graft/run"}
	var out []Outcome
	regions, err := converge.ReadRegions(cfg.Root)
	if err != nil {
		return nil, err
	}
	for _, a := range acc {
		o := Outcome{ID: a.ID, Section: a.Entry.Section, File: a.Entry.File, Samples: cfg.Samples}
		file := a.Entry.File
		reg, hasReg := byRec[a.ID]
		if cfg.Skeleton != nil {
			if !hasReg {
				// The skeleton fixes everything for this record: nothing to converge.
				o.Applied = true
				out = append(out, o)
				continue
			}
			file = reg.Path
			o.File = file
		}
		feedback := ""
		// A hand edit inside this record's region is refused before any model
		// call: converge would refuse to overwrite it anyway.
		for _, r := range regions {
			if r.Path == file && r.Owner.Record == a.ID && r.Edited() {
				return out, fmt.Errorf("%s: %w: %s was edited by hand; keep the edit by revising the record, or restore the region", a.Entry.Section, converge.ErrHandEdited, r.Path)
			}
		}
		body, ok, why := current(store, regions, a, file, cfg.Root)
		if ok && !pending[a.ID] && cfg.skelHash != "" && !cfg.skeletonCurrent(cfg.Log, a.ID, store, body) {
			why = "skeleton_hash" // owner unchanged, but the skeleton it was verified against moved
		} else if pending[a.ID] {
			why = "new"
		} else if ok {
			why = ""
		}
		if cfg.Skeleton != nil {
			o.Reason = why
		}
		if ok && !pending[a.ID] {
			cand, err := cfg.newCandidate(store, a.ID, file, body)
			if err != nil {
				return out, err
			}
			d, err := c.Converge(ctx, cand)
			if err != nil {
				return out, fmt.Errorf("%s: re-verify: %w", a.Entry.Section, err)
			}
			if d.Applied {
				o.Applied = true
				if why == "skeleton_hash" {
					cfg.say("%s (%s): skeleton changed, re-verified under the current gates (no model call)\n", a.Entry.Section, file)
				} else {
					cfg.say("%s (%s): up to date, re-verified under the current gates\n", a.Entry.Section, file)
				}
				out = append(out, o)
				continue
			}
			o.Regenerated = true
			feedback = "The existing file no longer passes these checks; fix it:\n" + failing(d)
			cfg.say("%s (%s): current body fails the current gates; regenerating\n", a.Entry.Section, file)
		}
		for o.Attempts < cfg.Attempts && !o.Applied {
			o.Attempts++
			cfg.say("%s (%s): attempt %d\n", a.Entry.Section, file, o.Attempts)
			var prompt string
			var err error
			if cfg.Skeleton != nil {
				prompt, err = cfg.renderPrompt(a, reg, feedback)
			} else {
				prompt, err = cfg.prompt(a, feedback)
			}
			if err != nil {
				return out, err
			}
			if cfg.Samples > 1 {
				ok, err := cfg.attemptSamples(ctx, c, store, a, file, regions, prompt, &o, &feedback)
				if err != nil {
					if o.LastFailure != "" {
						out = append(out, o)
					}
					return out, err
				}
				o.Applied = ok
				continue
			}
			o.Calls++
			resp, err := cfg.Model.Complete(ctx, prompt)
			if err != nil {
				if o.LastFailure != "" {
					// The code had already failed its checks; keep that on record.
					out = append(out, o)
				}
				return out, fmt.Errorf("%w: %v", ErrModel, err)
			}
			code := model.ExtractCode(resp, cfg.lang().Fence)
			if cfg.Skeleton != nil && hasPlaceholder(code) {
				// The seed is a placeholder, not an answer: refuse before any check runs.
				o.LastFailure = "the answer still contains the placeholder panic (" + recording.Placeholder + ")"
				feedback = "Your previous answer was refused before checking: " + o.LastFailure + "; write the real bodies"
				cfg.say("  not applied: %s\n", o.LastFailure)
				continue
			}
			cand, err := cfg.newCandidate(store, a.ID, file, code)
			if err != nil {
				return out, err
			}
			d, err := c.Converge(ctx, cand)
			switch {
			case errors.Is(err, converge.ErrMarkerInBody), errors.Is(err, converge.ErrSedumRegion):
				feedback = "Your previous answer was refused before checking: " + err.Error()
			case err != nil:
				return out, fmt.Errorf("%s: %w", a.Entry.Section, err)
			case d.Applied:
				o.Applied = true
				cfg.say("  applied (guarantee: %s)\n", d.Guarantee)
			default:
				o.LastFailure = failing(d)
				feedback = "Your previous answer failed these checks; fix it:\n" + o.LastFailure
				cfg.say("  not applied: %s\n", firstLine(o.LastFailure))
			}
		}
		out = append(out, o)
		if !o.Applied {
			// Later regions build on this one; stop rather than stack failures.
			return out, fmt.Errorf("pipeline: %s did not converge in %d attempts", a.Entry.Section, cfg.Attempts)
		}
	}
	return out, nil
}

func failing(d converge.Decision) string {
	var details []string
	for _, e := range d.Evidence {
		if e.Verdict != evidence.Pass {
			details = append(details, e.Detail)
		}
	}
	for _, k := range d.Result.Missing {
		details = append(details, "missing evidence: "+string(k))
	}
	return strings.Join(details, "\n")
}

// current reports whether the record's region already holds an unedited body
// written for the record's current version and gen_hash, and returns that body.
//
// When it does not, why names the first thing that moved: "gen_hash" when the
// region was written for another gen_hash, "version" for another version.
func current(store records.Store, regions []converge.Region, a Accepted, file, root string) (string, bool, string) {
	p, err := store.Current(a.ID)
	if err != nil {
		return "", false, ""
	}
	for _, r := range regions {
		if r.Path == file && r.Owner.Record == a.ID {
			switch {
			case r.Owner.Gen != p.GenHash:
				return "", false, "gen_hash"
			case r.Owner.Version != p.Version:
				return "", false, "version"
			case r.Edited():
				return "", false, ""
			}
			body, err := converge.RegionBody(root, r)
			return body, err == nil, ""
		}
	}
	return "", false, ""
}

// skeletonCurrent reports whether the region body was last verified against
// the current skeleton: the evidence log holds an entry for this record, its
// current gen_hash and this exact body that pins the current skeleton hash.
// Evidence from before skeletons were pinned has none, so it is not current;
// the body is re-verified under the gates (no model call) and fresh evidence
// pins the skeleton.
func (cfg Config) skeletonCurrent(log *evidence.Log, id string, store records.Store, body string) bool {
	p, err := store.Current(id)
	if err != nil || log == nil {
		return false
	}
	rh := converge.RegionHash(body)
	for _, e := range log.All() {
		if e.Pin.RecordID == id && e.Pin.GenHash == p.GenHash && e.Pin.RegionHash == rh && e.Pin.SkeletonHash == cfg.skelHash {
			return true
		}
	}
	return false
}

// fenceFor names a visible file's code fence from its extension (the checks
// may be in another language than the program, e.g. a Go smoke suite).
func fenceFor(name string) string {
	switch e := strings.TrimPrefix(filepath.Ext(name), "."); e {
	case "py":
		return "python"
	case "ts":
		return "typescript"
	case "js":
		return "javascript"
	default:
		return e
	}
}

// writeVisible adds the visible checks to a prompt, in name order.
func (cfg Config) writeVisible(b *strings.Builder) {
	if len(cfg.Visible) == 0 {
		return
	}
	names := make([]string, 0, len(cfg.Visible))
	for n := range cfg.Visible {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(b, "## Checks your code will be run against (visible; they run as a gate)\n\n")
	for _, n := range names {
		fmt.Fprintf(b, "### %s\n\n```%s\n%s```\n\n", n, fenceFor(n), cfg.Visible[n])
	}
}

func (cfg Config) say(f string, a ...any) {
	if cfg.Progress != nil {
		fmt.Fprintf(cfg.Progress, f, a...)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// lang returns the target language profile (Go when unset).
func (cfg Config) lang() lang.Profile {
	if cfg.Lang.Name == "" {
		return lang.Go
	}
	return cfg.Lang
}

// displayName is the language's name for the prompt.
func (cfg Config) displayName() string {
	if n := cfg.lang().Name; n != "go" {
		return n
	}
	return "Go"
}

// newCandidate is converge.NewCandidate with the language's comment prefix.
func (cfg Config) newCandidate(store records.Store, id, file, code string) (converge.Candidate, error) {
	cand, err := converge.NewCandidate(store, id, file, cfg.Tier, code)
	cand.Comment = cfg.lang().Comment
	cand.SkeletonHash = cfg.skelHash
	return cand, err
}

// bootstrap writes the profile's fresh-tree files (Go: go.mod) that are not
// there yet. (Under --render the scaffold comes from the generator package.)
func bootstrap(cfg Config) error {
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return err
	}
	mod := cfg.Module
	if mod == "" {
		mod = "app"
	}
	names := make([]string, 0, len(cfg.lang().Bootstrap))
	for n := range cfg.lang().Bootstrap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(cfg.Root, filepath.FromSlash(n))
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(lang.Expand(cfg.lang().Bootstrap[n], map[string]string{"module": mod})), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// prompt shows the model: the conventions, the whole spec as context, this
// record's obligations, the current tree, and any feedback from the last try.
// It never shows the oracle.
func (cfg Config) prompt(a Accepted, feedback string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "You are writing one file of a %s program, generated section by section from a spec.\n\n", cfg.displayName())
	fmt.Fprintf(&b, "## Conventions\n\n%s\n\n", strings.TrimSpace(cfg.Conventions))
	cfg.writeVisible(&b)
	fmt.Fprintf(&b, "## The whole spec (context)\n\n%s\n\n", strings.TrimSpace(cfg.Spec))
	fmt.Fprintf(&b, "## Your section: %s\n\nWrite the complete file `%s`. It must satisfy every statement below:\n\n", a.Entry.Section, a.Entry.File)
	for _, s := range a.Draft.Statements {
		fmt.Fprintf(&b, "- [%s] %s\n", s.Type, s.Text)
	}
	tree, err := sourceFiles(cfg.Root, a.Entry.File, cfg.lang())
	if err != nil {
		return "", err
	}
	if tree != "" {
		fmt.Fprintf(&b, "\n## Files already written (use what they declare; do not redeclare it)\n\n%s", tree)
	}
	if feedback != "" {
		fmt.Fprintf(&b, "\n## Feedback on your previous attempt\n\n%s\n", feedback)
	}
	fmt.Fprintf(&b, "\nAnswer with the complete contents of `%s` in a single ```%s block, %s. Do not include any line containing \"graft:synth\" or \"sedum:\".\n", a.Entry.File, cfg.lang().Fence, cfg.lang().Preamble)
	return b.String(), nil
}

// sourceFiles returns every source file of the language in root except skip,
// each in a fenced block.
func sourceFiles(root, skip string, l lang.Profile) (string, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	var names []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && l.IsSource(p) {
			rel, _ := filepath.Rel(root, p)
			if filepath.ToSlash(rel) != skip {
				names = append(names, rel)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		src, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "### %s\n\n```%s\n%s```\n\n", filepath.ToSlash(n), l.Fence, src)
	}
	return b.String(), nil
}
