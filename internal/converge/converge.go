package converge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/chad/graft/internal/evidence"
)

// Guarantee is what a convergence decision may claim. It is always "checked":
// SPEC §2 forbids the convergence layer from claiming a stronger guarantee.
const Guarantee = "checked"

// ErrHandEdited: the existing synthesis region was hand-edited since graft
// wrote it, so graft refuses to overwrite it (the same rule as sedum regions).
var ErrHandEdited = errors.New("converge: synthesis region was hand-edited; refusing to overwrite")

// Candidate is a proposed body for one record's synthesis region.
type Candidate struct {
	Path     string // file, relative to the tree root
	Comment  string // line-comment prefix for markers; "//" if empty
	RecordID string
	Version  int
	GenHash  string
	SpecHash string // hash of the specs the gates run (opaque; from the records layer)
	Tier     string // risk tier of the owning record
	Body     string
}

// Decision is the outcome of Converge.
type Decision struct {
	Applied   bool
	Guarantee string // always Guarantee
	Result    evidence.Result
	Evidence  []evidence.Evidence // what this run appended
}

// Converger runs candidates through gates and the evidence policy.
type Converger struct {
	Root   string // the working tree
	Log    *evidence.Log
	Policy evidence.Policy
	Gates  []Gate
	By     string // recorded on evidence; "graft/converge" if empty
}

// Converge writes the candidate into a scratch copy of the tree, runs every
// gate there, appends one evidence entry per gate pinned to the candidate
// (gen_hash, the new body's hash, spec_hash), and evaluates the tier's policy.
// Only when the policy is satisfied is the candidate written to Root.
// It refuses, before running anything, if the existing region was hand-edited.
func (c Converger) Converge(ctx context.Context, cand Candidate) (Decision, error) {
	d := Decision{Guarantee: Guarantee}
	if cand.Comment == "" {
		cand.Comment = "//"
	}
	if _, ok := c.Policy[cand.Tier]; !ok {
		return d, fmt.Errorf("%w: %q", evidence.ErrUnknownTier, cand.Tier)
	}
	if err := c.refuseHandEdits(cand); err != nil {
		return d, err
	}
	owner := Owner{Record: cand.RecordID, Version: cand.Version, Gen: cand.GenHash, Tier: cand.Tier}

	scratch, err := os.MkdirTemp("", "graft-converge-*")
	if err != nil {
		return d, err
	}
	defer os.RemoveAll(scratch)
	if err := copyTree(c.Root, scratch); err != nil {
		return d, err
	}
	if err := write(scratch, cand.Path, cand.Comment, owner, cand.Body); err != nil {
		return d, err
	}
	body := cand.Body
	if body != "" && body[len(body)-1] != '\n' {
		body += "\n"
	}
	pin := evidence.Pin{RecordID: cand.RecordID, Version: cand.Version, GenHash: cand.GenHash,
		RegionHash: HashBody(body), SpecHash: cand.SpecHash}
	by := c.By
	if by == "" {
		by = "graft/converge"
	}
	for _, g := range c.Gates {
		v, detail, err := g.Run(ctx, scratch)
		if err != nil {
			return d, err
		}
		e, err := c.Log.Append(evidence.Evidence{Kind: g.Kind(), Verdict: v, Pin: pin, By: by,
			Detail: fmt.Sprintf("%s: %s", g.Name(), detail)})
		if err != nil {
			return d, err
		}
		d.Evidence = append(d.Evidence, e)
	}
	if d.Result, err = c.Policy.Evaluate(c.Log, cand.Tier, pin); err != nil {
		return d, err
	}
	if !d.Result.Satisfied {
		return d, nil
	}
	if err := write(c.Root, cand.Path, cand.Comment, owner, cand.Body); err != nil {
		return d, err
	}
	d.Applied = true
	return d, nil
}

func (c Converger) refuseHandEdits(cand Candidate) error {
	p := filepath.Join(c.Root, filepath.FromSlash(cand.Path))
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rs, err := parse(cand.Path, data)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.Owner.Record == cand.RecordID && r.Edited() {
			return fmt.Errorf("%w: %s (record %s)", ErrHandEdited, cand.Path, cand.RecordID)
		}
	}
	return nil
}

// copyTree copies src into dst, skipping .git. Symlinks are not followed.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, info.Mode().Perm())
	})
}
