package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// renderTree plans the recording from the accepted records and the skeleton,
// renders it with sedum into cfg.Root, and seeds each region's _synth.go file
// if it does not exist yet (a converged body is never clobbered). It returns
// the regions by owning record id. It makes no model call.
func (cfg Config) renderTree(ctx context.Context, store records.Store, acc []Accepted) (map[string]recording.Region, map[string]bool, error) {
	if cfg.Sedum == nil || cfg.Generators == "" {
		return nil, nil, errors.New("pipeline: --render needs sedum and its generators")
	}
	if v, err := cfg.Sedum.Version(ctx); err == nil {
		cfg.say("sedum %s\n", v)
	}
	in := recording.Input{Skeleton: *cfg.Skeleton, Tier: cfg.Tier}
	for _, a := range acc {
		p, err := store.Current(a.ID)
		if err != nil {
			return nil, nil, err
		}
		in.Records = append(in.Records, recording.Rec{Pin: p, Section: a.Entry.Section})
	}
	planned, err := recording.Plan(in)
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: --render: %w", err)
	}
	var buf bytes.Buffer
	if err := recording.Write(&buf, planned.Recording); err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "graft-render-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	recPath := filepath.Join(tmp, "recording.json")
	if err := os.WriteFile(recPath, buf.Bytes(), 0o644); err != nil {
		return nil, nil, err
	}
	cleanup, err := cfg.sedumEnv()
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	if err := render.Render(ctx, cfg.Sedum, cfg.Root, recPath); err != nil {
		if errors.Is(err, render.ErrHandEdited) {
			return nil, nil, fmt.Errorf("pipeline: refusing to render before any model call: %w (restore the file, or revise the record)", err)
		}
		return nil, nil, fmt.Errorf("pipeline: --render: %w", err)
	}
	if err := cfg.Sedum.GrowCheck(ctx, cfg.Root); err != nil {
		return nil, nil, fmt.Errorf("pipeline: --render: grow --check after render: %w", err)
	}
	by := map[string]recording.Region{}
	for _, r := range planned.Regions {
		if !recording.IsSynthPath(r.Path) {
			return nil, nil, fmt.Errorf("pipeline: --render: region path %s is not a _synth.go file", r.Path)
		}
		by[r.Owner.RecordID] = r
		p := filepath.Join(cfg.Root, filepath.FromSlash(r.Path))
		if _, err := os.Stat(p); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(p, r.Seed, 0o644); err != nil {
			return nil, nil, err
		}
	}
	// Pending is decided from the region body, never from the whole file: a
	// seed written under another tier or pin differs in its marker only.
	regions, err := converge.ReadRegions(cfg.Root)
	if err != nil {
		return nil, nil, err
	}
	pending := map[string]bool{}
	for _, r := range planned.Regions {
		pending[r.Owner.RecordID] = true // no region found: treat as unconverged
		for _, g := range regions {
			if g.Path == r.Path && g.Owner.Record == r.Owner.RecordID {
				body, err := converge.RegionBody(cfg.Root, g)
				if err != nil {
					return nil, nil, err
				}
				pending[r.Owner.RecordID] = hasPlaceholder(body)
			}
		}
	}
	return by, pending, nil
}

// hasPlaceholder reports whether a body still holds a seeded stub.
func hasPlaceholder(body string) bool { return strings.Contains(body, recording.Placeholder) }

// sedumEnv gives sedum what it expects beside the tree: its generators and the
// records directory. Both are removed again, so the tree holds only code (the
// generators have their own go.mod and stay out of the committed tree).
func (cfg Config) sedumEnv() (func(), error) {
	root := cfg.Root
	gen := filepath.Join(root, "generators")
	prov := filepath.Join(root, "provenance")
	for _, p := range []string{gen, prov} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf("pipeline: --render: %s already exists in the tree", filepath.Base(p))
		}
	}
	// Copy under a temporary name and rename into place, so a failed or killed
	// copy never leaves a partial generators/ that blocks every later run.
	part := gen + ".partial"
	os.RemoveAll(part)
	if out, err := exec.Command("cp", "-r", cfg.Generators, part).CombinedOutput(); err != nil {
		os.RemoveAll(part)
		return nil, fmt.Errorf("pipeline: copy generators: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(part, gen); err != nil {
		os.RemoveAll(part)
		return nil, err
	}
	abs, err := filepath.Abs(filepath.Join(cfg.Repo, "provenance"))
	if err != nil {
		return nil, err
	}
	if err := os.Symlink(abs, prov); err != nil {
		os.RemoveAll(gen)
		return nil, err
	}
	return func() { os.RemoveAll(gen); os.Remove(prov) }, nil
}

// renderPrompt asks for one region file only: the Body helpers for the stubs
// the skeleton fixed. Everything else in the tree is read-only context.
func (cfg Config) renderPrompt(a Accepted, r recording.Region, feedback string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "You are writing the function bodies for one part of a Go program. A deterministic layer has already rendered the program's skeleton (types, imports, stubs, the contracts between files); you write only the helper functions the stubs delegate to.\n\n")
	fmt.Fprintf(&b, "## Conventions\n\n%s\n\n", strings.TrimSpace(cfg.Conventions))
	cfg.writeVisible(&b)
	fmt.Fprintf(&b, "## The whole spec (context)\n\n%s\n\n", strings.TrimSpace(cfg.Spec))
	fmt.Fprintf(&b, "## Your section: %s\n\nThe region file is `%s`. Its helpers must together satisfy every statement below:\n\n", a.Entry.Section, r.Path)
	for _, s := range a.Draft.Statements {
		fmt.Fprintf(&b, "- [%s] %s\n", s.Type, s.Text)
	}
	fmt.Fprintf(&b, "\n## Declares: the helpers you must implement, with exactly these signatures\n\n```go\n%s\n```\n", strings.Join(r.Declares, "\n"))
	tree, err := sourceFiles(cfg.Root, r.Path, lang.Go)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "\n## Skeleton and other files (read-only; use what they declare, do not redeclare it)\n\n%s", tree)
	if feedback != "" {
		fmt.Fprintf(&b, "\n## Feedback on your previous attempt\n\n%s\n", feedback)
	}
	fmt.Fprintf(&b, "\nAnswer with the complete contents of `%s` in a single ```go block: the package clause, the imports you need, the helpers above with their bodies, and any private helpers (named with the prefix `%s`). Do not write the skeleton's types or stubs, and do not include any line containing \"graft:synth\" or \"sedum:\".\n", r.Path, strings.TrimSuffix(filepath.Base(r.Path), "_synth.go"))
	return b.String(), nil
}
