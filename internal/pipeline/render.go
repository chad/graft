package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chad/graft/internal/contract"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// renderTree plans the recording from the accepted records and the skeleton,
// renders it with sedum into cfg.Root, and seeds each region's synth file
// if it does not exist yet (a converged body is never clobbered). It returns
// the regions by owning record id. It makes no model call.
//
// The skeleton lives in the records: cfg.Skeleton is only an import. Each file
// entry is written into the shape of the imprint that owns it (a new record
// version, only when it differs), and the tree is planned from the records.
// The returned string is the skeleton hash every region's evidence pins.
func (cfg Config) renderTree(ctx context.Context, store records.Store, acc []Accepted) (map[string]recording.Region, map[string]bool, string, map[string]bool, error) {
	nilRet := func(err error) (map[string]recording.Region, map[string]bool, string, map[string]bool, error) {
		return nil, nil, "", nil, err
	}
	if cfg.Sedum == nil || cfg.Generators == "" {
		return nilRet(errors.New("pipeline: --render needs sedum and its generators"))
	}
	if v, err := cfg.Sedum.Version(ctx); err == nil {
		cfg.say("sedum %s\n", v)
	}
	in := recording.Input{Skeleton: *cfg.Skeleton, Tier: cfg.Tier, Generator: cfg.lang().Name, Contract: cfg.Contract}
	if in.Contract == nil {
		b, ok := cfg.Sedum.(interface{ Bin() string })
		if !ok {
			return nilRet(errors.New("pipeline: --render: no convergence contract (Config.Contract) and the sedum is not a binary"))
		}
		gens, err := filepath.Abs(cfg.Generators)
		if err != nil {
			return nilRet(err)
		}
		gen := cfg.Skeleton.Generator
		if gen == "" {
			gen = cfg.lang().Name
		}
		in.Contract = contract.Exec{Bin: b.Bin(), Generators: gens, Package: gen, Ctx: ctx}
	}
	pins, err := cfg.currentPins(store, acc)
	if err != nil {
		return nilRet(err)
	}
	setRecs := func() {
		in.Records = in.Records[:0]
		for i, a := range acc {
			in.Records = append(in.Records, recording.Rec{Pin: pins[i], Section: a.Entry.Section})
		}
	}
	setRecs()
	// Check the imported skeleton as a whole before any record is revised.
	if _, err := recording.Plan(in); err != nil {
		return nilRet(fmt.Errorf("pipeline: --render: %w", err))
	}
	if err := cfg.importSkeleton(store, acc); err != nil {
		return nilRet(err)
	}
	if pins, err = cfg.currentPins(store, acc); err != nil {
		return nilRet(err)
	}
	if in.Skeleton, err = recording.SkeletonOf(cfg.Skeleton.Generator, pins); err != nil {
		return nilRet(err)
	}
	setRecs()
	gen := cfg.Skeleton.Generator
	if gen == "" {
		gen = cfg.lang().Name
	}
	skelHash, err := records.SkeletonHash(gen, pins)
	if err != nil {
		return nilRet(err)
	}
	planned, err := recording.Plan(in)
	if err != nil {
		return nilRet(fmt.Errorf("pipeline: --render: %w", err))
	}
	var buf bytes.Buffer
	if err := recording.Write(&buf, planned.Recording); err != nil {
		return nilRet(err)
	}
	tmp, err := os.MkdirTemp("", "graft-render-*")
	if err != nil {
		return nilRet(err)
	}
	defer os.RemoveAll(tmp)
	recPath := filepath.Join(tmp, "recording.json")
	if err := os.WriteFile(recPath, buf.Bytes(), 0o644); err != nil {
		return nilRet(err)
	}
	cleanup, err := cfg.sedumEnv()
	if err != nil {
		return nilRet(err)
	}
	defer cleanup()
	moved := cfg.movedSinceRecording(planned.Recording)
	const imported = " (the records already carry the imported shapes; running again after the fix is safe)"
	if err := render.Render(ctx, cfg.Sedum, cfg.Root, recPath); err != nil {
		if errors.Is(err, render.ErrHandEdited) {
			return nilRet(fmt.Errorf("pipeline: refusing to render before any model call: %w (restore the file, or revise the record)%s", err, imported))
		}
		return nilRet(fmt.Errorf("pipeline: --render: %w%s", err, imported))
	}
	if err := cfg.Sedum.GrowCheck(ctx, cfg.Root); err != nil {
		return nilRet(fmt.Errorf("pipeline: --render: grow --check after render: %w%s", err, imported))
	}
	by := map[string]recording.Region{}
	for _, r := range planned.Regions {
		if !recording.IsSynthPath(r.Path, planned.SynthSuffix) {
			return nilRet(fmt.Errorf("pipeline: --render: region path %s is not a synth file (suffix %q)", r.Path, planned.SynthSuffix))
		}
		by[r.Owner.RecordID] = r
		p := filepath.Join(cfg.Root, filepath.FromSlash(r.Path))
		if _, err := os.Stat(p); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nilRet(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nilRet(err)
		}
		if err := os.WriteFile(p, r.Seed, 0o644); err != nil {
			return nilRet(err)
		}
	}
	// Pending is decided from the region body, never from the whole file: a
	// seed written under another tier or pin differs in its marker only.
	regions, err := converge.ReadRegions(cfg.Root)
	if err != nil {
		return nilRet(err)
	}
	pending := map[string]bool{}
	for _, r := range planned.Regions {
		pending[r.Owner.RecordID] = true // no region found: treat as unconverged
		for _, g := range regions {
			if g.Path == r.Path && g.Owner.Record == r.Owner.RecordID {
				body, err := converge.RegionBody(cfg.Root, g)
				if err != nil {
					return nilRet(err)
				}
				pending[r.Owner.RecordID] = hasPlaceholder(body)
			}
		}
	}
	return by, pending, skelHash, moved, nil
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
	fmt.Fprintf(&b, "You are writing the function bodies for one part of a %s program. A deterministic layer has already rendered the program's skeleton (types, imports, stubs, the contracts between files); you write only the helper functions the stubs delegate to.\n\n", cfg.displayName())
	fmt.Fprintf(&b, "## Conventions\n\n%s\n\n", strings.TrimSpace(cfg.Conventions))
	cfg.writeVisible(&b)
	fmt.Fprintf(&b, "## The whole spec (context)\n\n%s\n\n", strings.TrimSpace(cfg.Spec))
	fmt.Fprintf(&b, "## Your section: %s\n\nThe region file is `%s`. Its helpers must together satisfy every statement below:\n\n", a.Entry.Section, r.Path)
	for _, s := range a.Draft.Statements {
		fmt.Fprintf(&b, "- [%s] %s\n", s.Type, s.Text)
	}
	fmt.Fprintf(&b, "\n## Declares: the helpers you must implement, with exactly these signatures\n\n```%s\n%s\n```\n", cfg.lang().Fence, strings.Join(r.Declares, "\n"))
	tree, err := sourceFiles(cfg.Root, r.Path, cfg.lang())
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "\n## Skeleton and other files (read-only; use what they declare, do not redeclare it)\n\n%s", tree)
	if feedback != "" {
		fmt.Fprintf(&b, "\n## Feedback on your previous attempt\n\n%s\n", feedback)
	}
	stem := strings.TrimSuffix(filepath.Base(r.Path), filepath.Ext(r.Path))
	fmt.Fprintf(&b, "\nAnswer with the complete contents of `%s` in a single ```%s block, %s, then the imports you need, the helpers above with their bodies, and any private helpers (named with the prefix `%s`). Do not write the skeleton's types or stubs, and do not include any line containing \"graft:synth\" or \"sedum:\".\n", r.Path, cfg.lang().Fence, cfg.lang().Preamble, strings.TrimSuffix(stem, r.SynthSuffix))
	return b.String(), nil
}

// currentPins reads the current pin of every accepted record, in order.
func (cfg Config) currentPins(store records.Store, acc []Accepted) ([]records.Pin, error) {
	pins := make([]records.Pin, len(acc))
	for i, a := range acc {
		p, err := store.Current(a.ID)
		if err != nil {
			return nil, err
		}
		pins[i] = p
	}
	return pins, nil
}

// importSkeleton writes cfg.Skeleton into the records: each file entry becomes
// part of the shape of the imprint that owns it, and a record the skeleton
// no longer mentions loses its shape. A record whose shape is already what the
// skeleton says is not touched, so importing the same skeleton again is a
// no-op and bumps no version.
func (cfg Config) importSkeleton(store records.Store, acc []Accepted) error {
	ws, ok := store.(records.ShapeStore)
	if !ok {
		return errors.New("pipeline: --render: the record store cannot write shapes")
	}
	bySection := map[string]string{}
	planned := map[string]bool{}
	for _, a := range acc {
		bySection[a.Entry.Section] = a.ID
		planned[a.ID] = true
	}
	shapes, err := recording.ShapesOf(*cfg.Skeleton, func(f recording.File) (string, error) {
		switch {
		case f.Record != "" && f.Section != "":
			return "", fmt.Errorf("pipeline: --render: file %s sets both record and section", f.Path)
		case f.Record != "":
			if !planned[f.Record] {
				return "", fmt.Errorf("pipeline: --render: file %s names record %q, which is not planned", f.Path, f.Record)
			}
			return f.Record, nil
		case f.Section != "":
			id, ok := bySection[f.Section]
			if !ok {
				return "", fmt.Errorf("pipeline: --render: file %s names section %q, which no record covers", f.Path, f.Section)
			}
			return id, nil
		}
		return "", fmt.Errorf("pipeline: --render: file %s names neither a record nor a section", f.Path)
	})
	if err != nil {
		return err
	}
	for _, a := range acc {
		changed, err := ws.SetShape(a.ID, shapes[a.ID], "import skeleton", "graft")
		if err != nil {
			return fmt.Errorf("pipeline: --render: %w", err)
		}
		if changed {
			cfg.say("%s: shape imported (%s now at a new version)\n", a.Entry.Section, a.ID)
		}
	}
	return nil
}

// movedSinceRecording finds the records whose planned invocations differ from
// the recording sedum kept in the tree from the last render (.sedum/recordings).
// It is how a first --render run after upgrading from before M11a (markers
// without a shape) tells "only the skeleton was imported" from "this record's
// own shape was edited as well": the old recording holds the stub docs,
// signatures and declarations the body was written against. A record with no
// readable old recording is not listed (nothing to compare; it re-verifies and
// regenerates only if its body fails).
func (cfg Config) movedSinceRecording(planned recording.Recording) map[string]bool {
	moved := map[string]bool{}
	for _, r := range planned.Records {
		b, err := os.ReadFile(filepath.Join(cfg.Root, ".sedum", "recordings", r.RecordID+".json"))
		if err != nil {
			continue
		}
		var old recording.Recording
		if json.Unmarshal(b, &old) != nil {
			continue
		}
		for _, o := range old.Records {
			if o.RecordID != r.RecordID {
				continue
			}
			a, _ := json.Marshal(o.Phases)
			n, _ := json.Marshal(r.Phases)
			if !bytes.Equal(a, n) {
				moved[r.RecordID] = true
			}
		}
	}
	return moved
}
