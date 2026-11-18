package converge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// Guarantee is what a convergence decision may claim. It is always "checked":
// SPEC §2 forbids the convergence layer from claiming a stronger guarantee.
const Guarantee = "checked"

var (
	// ErrHandEdited: the existing synthesis region was hand-edited since graft
	// wrote it, so graft refuses to overwrite it (the same rule as sedum regions).
	ErrHandEdited = errors.New("converge: synthesis region was hand-edited; refusing to overwrite")
	// ErrNoGates: nothing would be checked, so "checked" would be a false claim.
	ErrNoGates = errors.New("converge: no gates configured; refusing to claim a candidate was checked")
	// ErrUncovered: the tier requires an executable kind no gate in this run produces.
	ErrUncovered = errors.New("converge: the tier requires an executable check no gate provides")
	// ErrChanged: the tree changed while gates ran; their verdicts do not cover it.
	ErrChanged = errors.New("converge: tree changed during convergence; not applied")
	// ErrBadPath: the candidate path is absolute, escapes the root, or is a symlink.
	ErrBadPath = errors.New("converge: candidate path must be a regular file inside the root")
	// ErrMarkerInBody: the body contains a synthesis or sedum marker.
	ErrMarkerInBody = errors.New("converge: candidate body contains a synthesis or sedum marker")
	// ErrStalePin: the candidate's version/gen_hash is not the record's current one.
	ErrStalePin = errors.New("converge: candidate pin is not the record's current version")
	// ErrNoRecords: a Converger must read pins from the records layer.
	ErrNoRecords = errors.New("converge: Converger.Records is required")
)

// Executable kinds are produced by running something. Evidence of these
// kinds must come from THIS run's gates: an earlier run checked an earlier
// tree. Other kinds (a human sign-off) are taken from fresh evidence in the log.
var executable = map[evidence.Kind]bool{
	evidence.TestRun: true, evidence.Eval: true, evidence.StaticAnalysis: true, evidence.Conformance: true,
}

// Executable reports whether evidence of kind k is produced by running
// something, and so may be claimed by a gate.
func Executable(k evidence.Kind) bool { return executable[k] }

// ErrGateKind: a gate claims a non-executable kind (a human sign-off cannot
// be produced by running a command).
var ErrGateKind = errors.New("converge: a gate can only produce an executable evidence kind")

// Candidate is a proposed body for one record's synthesis region.
type Candidate struct {
	Path     string // file, relative to the tree root
	Comment  string // line-comment prefix for markers; "//" if empty
	RecordID string
	Version  int
	GenHash  string
	SpecHash string // the record's spec hash (records.Pin.SpecHash); must match the store
	Tier     string // risk tier of the owning record
	Body     string
}

// Decision is the outcome of Converge.
type Decision struct {
	Applied   bool
	Guarantee string // always Guarantee
	Result    evidence.Result
	Evidence  []evidence.Evidence // what this run appended
	// Checked is set when the first gate starts. A failure with Checked
	// false was a refusal before checking; with Checked true, checking had
	// begun (even if it was cancelled before any evidence was written).
	Checked bool
}

// Converger runs candidates through gates and the evidence policy.
type Converger struct {
	Root    string        // the working tree
	Records records.Store // source of truth for every candidate's version, gen_hash and spec hash
	Log     *evidence.Log
	Policy  evidence.Policy
	Gates   []Gate
	By      string // recorded on evidence; "graft/converge" if empty
}

// Converge writes the candidate into a scratch copy of the tree, runs every
// gate there, and appends one evidence entry per gate pinned to the candidate.
// The candidate reaches Root only when every gate in THIS run passed, every
// executable kind the tier requires came from this run, every other required
// kind has fresh passing evidence, and the tree is unchanged since the copy.
func (c Converger) Converge(ctx context.Context, cand Candidate) (Decision, error) {
	d := Decision{Guarantee: Guarantee}
	if cand.Comment == "" {
		cand.Comment = "//"
	}
	required, ok := c.Policy[cand.Tier]
	if !ok {
		return d, fmt.Errorf("%w: %q", evidence.ErrUnknownTier, cand.Tier)
	}
	if len(c.Gates) == 0 {
		return d, ErrNoGates
	}
	if isNil(c.Records) {
		return d, ErrNoRecords
	}
	if err := c.checkPin(cand); err != nil {
		return d, err
	}
	gated := map[evidence.Kind]bool{}
	for _, g := range c.Gates {
		if !executable[g.Kind()] {
			return d, fmt.Errorf("%w: gate %s claims %q", ErrGateKind, g.Name(), g.Kind())
		}
		gated[g.Kind()] = true
	}
	for _, k := range required {
		if executable[k] && !gated[k] {
			return d, fmt.Errorf("%w: tier %q requires %s", ErrUncovered, cand.Tier, k)
		}
	}
	if strings.Contains(cand.Body, beginTag) || strings.Contains(cand.Body, endTag) || hasSedumMarker(cand.Body) {
		return d, ErrMarkerInBody
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
	next, err := renderRegion(cand.Path, before, cand.Comment, owner, cand.Body)
	if err != nil {
		return d, err
	}
	pin := evidence.Pin{RecordID: cand.RecordID, Version: cand.Version, GenHash: cand.GenHash,
		RegionHash: HashBody(normalizeBody(cand.Body)), SpecHash: cand.SpecHash}
	if err := checkRendered(cand, next, pin.RegionHash); err != nil {
		return d, err
	}
	if err := guardSedum(cand, before, next); err != nil {
		return d, err
	}

	skip := c.skip()
	digest, err := treeDigest(c.Root, skip)
	if err != nil {
		return d, err
	}
	scratch, err := os.MkdirTemp("", "graft-converge-*")
	if err != nil {
		return d, err
	}
	defer os.RemoveAll(scratch)
	if err := copyTree(c.Root, scratch, skip); err != nil {
		return d, err
	}
	st := filepath.Join(scratch, filepath.FromSlash(cand.Path))
	if err := os.MkdirAll(filepath.Dir(st), 0o755); err != nil {
		return d, err
	}
	if err := os.WriteFile(st, next, 0o644); err != nil {
		return d, err
	}

	by := c.By
	if by == "" {
		by = "graft/converge"
	}
	verdicts := map[evidence.Kind]evidence.Verdict{}
	d.Checked = true
	for _, g := range c.Gates {
		v, detail, err := g.Run(ctx, scratch)
		if err != nil {
			return d, err // the gate could not run: no verdict, nothing applied
		}
		e, err := c.Log.Append(evidence.Evidence{Kind: g.Kind(), Verdict: v, Pin: pin, By: by,
			Detail: fmt.Sprintf("%s: %s", g.Name(), detail)})
		if err != nil {
			return d, err
		}
		d.Evidence = append(d.Evidence, e)
		if prev, seen := verdicts[g.Kind()]; !seen || prev == evidence.Pass {
			verdicts[g.Kind()] = v // any non-pass of a kind wins: no masking
		}
	}
	if d.Result, err = c.decide(required, verdicts, pin); err != nil {
		return d, err
	}
	if !d.Result.Satisfied {
		return d, nil
	}
	if err := c.apply(cand, target, before, next, digest, skip); err != nil {
		return d, err
	}
	d.Applied = true
	return d, nil
}

// isNil also catches a typed nil (a nil *T stored in the interface).
func isNil(s records.Store) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// checkPin re-reads the record: the candidate's version, gen_hash and spec hash must be
// its current ones. Caller-supplied literals are never trusted.
func (c Converger) checkPin(cand Candidate) error {
	cur, err := c.Records.Current(cand.RecordID)
	if err != nil {
		return err
	}
	if cur.Version != cand.Version || cur.GenHash != cand.GenHash || cur.SpecHash != cand.SpecHash {
		return fmt.Errorf("%w: %s is at v%d %s specs %q, candidate pins v%d %s specs %q",
			ErrStalePin, cand.RecordID, cur.Version, cur.GenHash, cur.SpecHash, cand.Version, cand.GenHash, cand.SpecHash)
	}
	return nil
}

// hasSedumMarker reports whether any body line is a sedum marker, by the
// render layer's own rules (one definition of what a marker is).
func hasSedumMarker(body string) bool {
	for _, l := range strings.Split(body, "\n") {
		if render.IsMarkerLine(l) {
			return true
		}
	}
	return false
}

// decide combines this run's verdicts with fresh log evidence for the
// non-executable kinds. Every gate must pass, required or not.
func (c Converger) decide(required []evidence.Kind, verdicts map[evidence.Kind]evidence.Verdict, pin evidence.Pin) (evidence.Result, error) {
	if err := c.Log.Verify(); err != nil {
		return evidence.Result{}, err
	}
	r := evidence.Result{Satisfied: true}
	kinds := make([]evidence.Kind, 0, len(verdicts))
	for k := range verdicts {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	for _, k := range kinds {
		if verdicts[k] != evidence.Pass {
			r.Failing = append(r.Failing, k)
			r.Satisfied = false
		}
	}
	latest := map[evidence.Kind]evidence.Evidence{}
	for _, e := range c.Log.Fresh(pin) {
		latest[e.Kind] = e
	}
	for _, k := range required {
		if executable[k] {
			continue // decided by this run above
		}
		e, ok := latest[k]
		switch {
		case !ok:
			r.Missing = append(r.Missing, k)
			r.Satisfied = false
		case e.Verdict != evidence.Pass:
			r.Failing = append(r.Failing, k)
			r.Satisfied = false
		}
	}
	return r, nil
}

// checkRendered re-parses the rendered file: exactly one region for the
// record, and its bytes hash to what the evidence will be pinned to.
func checkRendered(cand Candidate, next []byte, regionHash string) error {
	rs, err := parse(cand.Path, next)
	if err != nil {
		return err
	}
	n := 0
	for _, r := range rs {
		if r.Owner.Record == cand.RecordID {
			n++
			if r.Actual != regionHash || r.Edited() {
				return fmt.Errorf("%w: rendered region does not hash to its pin", ErrMarkerInBody)
			}
		}
	}
	if n != 1 {
		return fmt.Errorf("%w: rendered file has %d regions for %s", ErrDuplicateRegion, n, cand.RecordID)
	}
	return nil
}

// lockPath is the per-tree lock file that serialises graft's own convergence
// runs on a tree. It lives in the system temp dir, keyed by the tree's
// absolute path, so it never appears in (or perturbs the digest of) the tree.
// It does not serialise editors: the real guard against a concurrent edit is
// the content re-check (whole-tree digest, then the target's bytes) made
// immediately before the rename.
func lockPath(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), "graft-converge-"+hex.EncodeToString(sum[:8])+".lock"), nil
}

// apply writes next to target only if the tree still has the digest the gates
// ran against and, read after that, the target still holds before. The write
// is a fresh temp file in the target's directory (os.CreateTemp: never a
// planted name), fsync, chmod, rename, then a directory fsync.
func (c Converger) apply(cand Candidate, target string, before, next []byte, digest string, skip map[string]bool) error {
	rel := cand.Path
	lp, err := lockPath(c.Root)
	if err != nil {
		return err
	}
	lf, err := os.OpenFile(lp, lockOpenFlags, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := lockFile(lf); err != nil {
		return err
	}
	defer unlockFile(lf)

	if _, err := c.target(rel); err != nil { // re-check: no symlink appeared in the path
		return err
	}
	// The record (version, gen_hash, spec hash) must not have moved while gates ran. Residual window: record
	// writers (provenance) do not take graft's lock, so a record can still
	// change between this check and the rename below. The evidence written by
	// this run stays pinned to the version it checked, and goes stale (§7) the
	// moment gen_hash moves, so a late change is never silently covered.
	if err := c.checkPin(cand); err != nil {
		return err
	}
	if d, err := treeDigest(c.Root, skip); err != nil {
		return err
	} else if d != digest {
		return ErrChanged
	}
	mode := fs.FileMode(0o644)
	now, err := readOptional(target)
	if err != nil {
		return err
	}
	if string(now) != string(before) {
		return ErrChanged
	}
	if fi, err := os.Lstat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(target)
	created, err := mkdirAllTracked(c.Root, dir)
	if err != nil {
		return err
	}
	cleanup := func() { // remove directories this apply created, deepest first
		for i := len(created) - 1; i >= 0; i-- {
			os.Remove(created[i])
		}
	}
	t, err := os.CreateTemp(dir, ".graft-*.tmp")
	if err != nil {
		cleanup()
		return err
	}
	tmp := t.Name()
	fail := func(err error) error { t.Close(); os.Remove(tmp); cleanup(); return err }
	if _, err := t.Write(next); err != nil {
		return fail(err)
	}
	if err := t.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := t.Sync(); err != nil {
		return fail(err)
	}
	if err := t.Close(); err != nil {
		os.Remove(tmp)
		cleanup()
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		cleanup()
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// mkdirAllTracked creates dir (inside root) and returns the directories it
// created, outermost first, so a failed apply can remove exactly those.
func mkdirAllTracked(root, dir string) ([]string, error) {
	var missing []string
	for d := dir; d != root && d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append([]string{d}, missing...)
	}
	for i, d := range missing {
		if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			for j := i - 1; j >= 0; j-- {
				os.Remove(missing[j])
			}
			return nil, err
		}
	}
	return missing, nil
}

// skip lists root-relative paths excluded from the digest and the scratch
// copy: the evidence log and its head, when they live inside the tree.
func (c Converger) skip() map[string]bool {
	s := map[string]bool{}
	if c.Log == nil {
		return s
	}
	// Resolve both: an absolute root with a relative log path (or the
	// reverse) must still recognise the log as inside the tree.
	root, err1 := filepath.Abs(c.Root)
	logp, err2 := filepath.Abs(c.Log.Path())
	if err1 != nil || err2 != nil {
		return s
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(logp)); err == nil {
		logp = filepath.Join(d, filepath.Base(logp))
	}
	rel, err := filepath.Rel(root, logp)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return s
	}
	rel = filepath.ToSlash(rel)
	s[rel], s[rel+".head"], s[rel+".head.tmp"] = true, true, true
	return s
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

// treeDigest hashes every regular file's path and bytes and every symlink's
// path and target under root (skipping .git and skip), in walk order.
//
// Cost: it reads the whole tree, twice per Converge (before the copy and
// before the write). On trees with large generated directories (node_modules,
// build output) that is slow, and files that churn on their own (caches,
// logs) cause spurious ErrChanged. Narrowing the digest to declared inputs is
// a follow-up.
func treeDigest(root string, skip map[string]bool) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if skip[rel] {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "L\x00%s\x00%s\x00", rel, to)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			fmt.Fprintf(h, "F\x00%s\x00%x\x00", rel, sum)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// copyTree copies src into dst, skipping .git and skip. Symlinks are
// recreated, never followed; an absolute link that points inside src is
// rewritten to the same place inside dst, so a gate cannot reach the real
// tree through it. The scratch copy isolates the WORKING TREE from gates; it
// is not a sandbox: a gate runs with your permissions and environment.
func copyTree(src, dst string, skip map[string]bool) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
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
		if skip[filepath.ToSlash(rel)] {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if filepath.IsAbs(to) {
				if r, err := filepath.Rel(srcAbs, to); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
					to = filepath.Join(dst, r)
				}
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
