package converge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/chad/graft/internal/evidence"
)

// Guarantee is what a convergence decision may claim. It is always "checked":
// SPEC §2 forbids the convergence layer from claiming a stronger guarantee.
const Guarantee = "checked"

// ErrHandEdited: the existing synthesis region was hand-edited since graft
// wrote it, so graft refuses to overwrite it (the same rule as sedum regions).
var ErrHandEdited = errors.New("converge: synthesis region was hand-edited; refusing to overwrite")

// ErrNoGates: nothing would be checked, so "checked" would be a false claim.
var ErrNoGates = errors.New("converge: no gates configured; refusing to claim a candidate was checked")

// ErrChanged: the target file changed while gates ran; the result no longer
// speaks for what is on disk, so nothing is written.
var ErrChanged = errors.New("converge: target file changed during convergence; not applied")

// ErrBadPath: the candidate path is absolute, escapes the root, or is a symlink.
var ErrBadPath = errors.New("converge: candidate path must be a regular file inside the root")

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
	if len(c.Gates) == 0 {
		return d, ErrNoGates
	}
	target, err := c.target(cand.Path)
	if err != nil {
		return d, err
	}
	before, err := readOptional(target)
	if err != nil {
		return d, err
	}
	if err := refuseHandEdits(cand, before); err != nil {
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
	// The gates checked the tree as it was when copied. If the target changed
	// since (a hand edit, another writer), that check does not cover it.
	now, err := readOptional(target)
	if err != nil {
		return d, err
	}
	if string(now) != string(before) {
		return d, ErrChanged
	}
	if err := write(c.Root, cand.Path, cand.Comment, owner, cand.Body); err != nil {
		return d, err
	}
	d.Applied = true
	return d, nil
}

// target resolves a candidate path inside Root, refusing absolute paths,
// escapes and symlinks.
func (c Converger) target(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrBadPath, rel)
	}
	p := filepath.Join(c.Root, clean)
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrBadPath, rel)
	}
	// No parent directory may be a symlink either: it could lead outside Root.
	for dir := filepath.Dir(clean); dir != "."; dir = filepath.Dir(dir) {
		if fi, err := os.Lstat(filepath.Join(c.Root, dir)); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %q passes through a symlink", ErrBadPath, rel)
		}
	}
	return p, nil
}

func readOptional(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func refuseHandEdits(cand Candidate, data []byte) error {
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

// copyTree copies src into dst, skipping .git. Symlinks are recreated as
// symlinks (never followed), so the scratch tree has the same shape. The
// scratch copy isolates the WORKING TREE from gates; it is not a sandbox: a
// gate is a command you chose and runs with your permissions and environment.
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
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(to, filepath.Join(dst, rel))
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
