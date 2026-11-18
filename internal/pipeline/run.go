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

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/records"
)

// Config is one `graft run`.
type Config struct {
	Repo        string // provenance repo (records + .graft index)
	Root        string // the code tree being generated
	Module      string // Go module path for a fresh tree
	Spec        string // the full spec text, shown to the model as context
	Conventions string // architecture conventions, shown to the model
	Tier        string
	Attempts    int // per record; 0 means 3
	Model       model.Model
	Gates       []converge.Gate
	Policy      evidence.Policy
	Log         *evidence.Log
	Progress    io.Writer // nil for silence
}

// Outcome is what happened to one record.
type Outcome struct {
	ID, Section, File string
	Applied           bool
	Attempts          int
	LastFailure       string
}

// Run converges one region per accepted record, in spec order. A record whose
// region already holds a body that still satisfies its pin is left alone.
func Run(ctx context.Context, cfg Config, acc []Accepted) ([]Outcome, error) {
	if cfg.Attempts == 0 {
		cfg.Attempts = 3
	}
	if err := bootstrap(cfg); err != nil {
		return nil, err
	}
	store, err := records.OpenDir(cfg.Repo)
	if err != nil {
		return nil, err
	}
	c := converge.Converger{Root: cfg.Root, Records: store, Log: cfg.Log, Policy: cfg.Policy, Gates: cfg.Gates, By: "graft/run"}
	sort.Slice(acc, func(i, j int) bool { return acc[i].Entry.Order < acc[j].Entry.Order })
	var out []Outcome
	regions, err := converge.ReadRegions(cfg.Root)
	if err != nil {
		return nil, err
	}
	for _, a := range acc {
		o := Outcome{ID: a.ID, Section: a.Entry.Section, File: a.Entry.File}
		if current(store, regions, a) {
			o.Applied = true
			cfg.say("%s (%s): up to date\n", a.Entry.Section, a.Entry.File)
			out = append(out, o)
			continue
		}
		feedback := ""
		for o.Attempts < cfg.Attempts && !o.Applied {
			o.Attempts++
			cfg.say("%s (%s): attempt %d\n", a.Entry.Section, a.Entry.File, o.Attempts)
			prompt, err := cfg.prompt(a, feedback)
			if err != nil {
				return out, err
			}
			resp, err := cfg.Model.Complete(ctx, prompt)
			if err != nil {
				return out, err
			}
			cand, err := converge.NewCandidate(store, a.ID, a.Entry.File, cfg.Tier, model.ExtractCode(resp, "go"))
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
				var details []string
				for _, e := range d.Evidence {
					if e.Verdict != evidence.Pass {
						details = append(details, e.Detail)
					}
				}
				feedback = "Your previous answer failed these checks; fix it:\n" + strings.Join(details, "\n")
				o.LastFailure = strings.Join(details, "\n")
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

// current reports whether the record's region already holds an unedited body
// written for the record's current version and gen_hash: nothing to redo.
func current(store records.Store, regions []converge.Region, a Accepted) bool {
	p, err := store.Current(a.ID)
	if err != nil {
		return false
	}
	for _, r := range regions {
		if r.Path == a.Entry.File && r.Owner.Record == a.ID {
			return r.Owner.Version == p.Version && r.Owner.Gen == p.GenHash && !r.Edited()
		}
	}
	return false
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

// bootstrap gives a fresh tree its go.mod. (When sedum rendering lands, the
// scaffold comes from the generator package instead.)
func bootstrap(cfg Config) error {
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return err
	}
	gm := filepath.Join(cfg.Root, "go.mod")
	if _, err := os.Stat(gm); err == nil {
		return nil
	}
	mod := cfg.Module
	if mod == "" {
		mod = "app"
	}
	return os.WriteFile(gm, []byte("module "+mod+"\n\ngo 1.22\n"), 0o644)
}

// prompt shows the model: the conventions, the whole spec as context, this
// record's obligations, the current tree, and any feedback from the last try.
// It never shows the oracle.
func (cfg Config) prompt(a Accepted, feedback string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "You are writing one file of a Go program, generated section by section from a spec.\n\n")
	fmt.Fprintf(&b, "## Conventions\n\n%s\n\n", strings.TrimSpace(cfg.Conventions))
	fmt.Fprintf(&b, "## The whole spec (context)\n\n%s\n\n", strings.TrimSpace(cfg.Spec))
	fmt.Fprintf(&b, "## Your section: %s\n\nWrite the complete file `%s`. It must satisfy every statement below:\n\n", a.Entry.Section, a.Entry.File)
	for _, s := range a.Draft.Statements {
		fmt.Fprintf(&b, "- [%s] %s\n", s.Type, s.Text)
	}
	tree, err := goFiles(cfg.Root, a.Entry.File)
	if err != nil {
		return "", err
	}
	if tree != "" {
		fmt.Fprintf(&b, "\n## Files already written (use what they declare; do not redeclare it)\n\n%s", tree)
	}
	if feedback != "" {
		fmt.Fprintf(&b, "\n## Feedback on your previous attempt\n\n%s\n", feedback)
	}
	fmt.Fprintf(&b, "\nAnswer with the complete contents of `%s` in a single ```go block, starting with the package clause. Do not include any line containing \"graft:synth\" or \"sedum:\".\n", a.Entry.File)
	return b.String(), nil
}

// goFiles returns every .go file in root except skip, each in a fenced block.
func goFiles(root, skip string) (string, error) {
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
		if !d.IsDir() && strings.HasSuffix(p, ".go") {
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
		fmt.Fprintf(&b, "### %s\n\n```go\n%s```\n\n", filepath.ToSlash(n), src)
	}
	return b.String(), nil
}
