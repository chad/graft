// Package records is graft's view of provenance records: stable pins and
// staleness. Schema, canonical hashing and gen_hash come from
// github.com/livecodelife/provenance/record; nothing is reimplemented here.
package records

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/livecodelife/provenance/record"
)

// ErrNotFound means the record, or the requested version, does not exist.
var ErrNotFound = errors.New("records: not found")

// ErrSpecMissing means an associated spec could not be read (absent, not a
// regular file, permission denied, or reached through a symlink leaving the
// repository), so the record's spec hash cannot be stated. It is never
// silently treated as "no specs".
var ErrSpecMissing = errors.New("records: associated spec cannot be resolved")

// Pin identifies a record version and the generation-driving content it had.
type Pin struct {
	RecordID string // stable id, never derived from content
	Version  int
	GenHash  string // hash over generation-driving fields only
	// SpecHash summarises the record's associated specs: "" when it has none,
	// otherwise "sha256:" + hex(sha256(JSON array of provenance's per-spec
	// hashes, in record order)). Specs verify output and do not drive it, so
	// Stale ignores this; evidence pins it (SPEC §7).
	SpecHash string
	// Type is the record's type ("brief", "blueprint", "imprint"); Status its
	// lifecycle status; AffectedScope its declared file scope. They do not
	// drive generation; planning reads them to decide who may own code.
	Type          string
	Status        string
	AffectedScope []string
	// Shape is the skeleton the record owns: one entry per file, each a tree
	// of strings, lists and maps (provenance record.Record.Shape). It drives
	// generation, so it is part of GenHash; planning renders from it.
	Shape []any
}

// Store reads records from a provenance repo on disk
// (provenance/<id>.yml + provenance/.versions/<id>/).
type Store interface {
	Current(id string) (Pin, error)                  // ErrNotFound if unknown
	Snapshot(id string, version int) ([]byte, error) // immutable snapshot bytes
}

type dirStore struct {
	fs   *record.FilesStore
	root string
}

// OpenDir opens the provenance repo rooted at dir (the directory that holds
// provenance/).
func OpenDir(dir string) (Store, error) {
	fi, err := os.Stat(filepath.Join(dir, "provenance"))
	if err != nil {
		return nil, fmt.Errorf("records: open %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("records: open %q: provenance is not a directory", dir)
	}
	return &dirStore{fs: &record.FilesStore{Root: dir}, root: dir}, nil
}

// validID rejects ids that could escape the records directory.
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`) && !strings.ContainsRune(id, 0)
}

func (d *dirStore) Current(id string) (Pin, error) {
	p, r, err := d.currentGen(id)
	if err != nil {
		return Pin{}, err
	}
	sh, err := specHash(r, confined(d.root, d.fs.Specs()))
	if err != nil {
		return Pin{}, fmt.Errorf("%s: %w", id, err)
	}
	p.SpecHash = sh
	return p, nil
}

// currentGen reads the record's id, version and gen_hash without touching its
// specs: generation staleness must not depend on whether a spec file exists.
func (d *dirStore) currentGen(id string) (Pin, *record.Record, error) {
	if !validID(id) {
		return Pin{}, nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r, _, err := d.fs.Load(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Pin{}, nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Pin{}, nil, fmt.Errorf("records: load %s: %w", id, err)
	}
	if r.ID != id {
		return Pin{}, nil, fmt.Errorf("records: %s.yml holds record %q", id, r.ID)
	}
	v := r.Version
	if v == 0 {
		v = 1 // a v1 record reads as version 1
	}
	return Pin{RecordID: r.ID, Version: v, GenHash: r.GenHash(), Type: string(r.Type),
		Status: string(r.Status), AffectedScope: append([]string(nil), r.AffectedScope...), Shape: r.Shape}, r, nil
}

// ErrSpecPath means an associated spec path is absolute or escapes the repo.
var ErrSpecPath = errors.New("records: associated spec path is outside the repository")

// confined wraps provenance's resolver so a spec path cannot be absolute,
// climb out of the repository, or reach outside it through a symlink: a record
// must not be able to make graft read, and pin evidence to, an arbitrary file.
// Such a path resolves as missing (the caller has already reported lexical
// escapes as ErrSpecPath).
func confined(root string, resolve record.SpecResolver) record.SpecResolver {
	return func(p string) (string, bool) {
		if !specPathOK(p) || !insideAfterSymlinks(root, p) {
			return "", false
		}
		return resolve(p)
	}
}

func insideAfterSymlinks(root, p string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	t, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		return false // missing or unreadable: reported as missing
	}
	rel, err := filepath.Rel(r, t)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func specPathOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.Contains(p, "\\") {
		return false
	}
	c := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	return c != ".." && !strings.HasPrefix(c, "../")
}

// specHash aggregates provenance's per-spec hashes (record.SpecHashes, which
// also handles record-file specs) into one value for the pin.
func specHash(r *record.Record, resolve record.SpecResolver) (string, error) {
	if len(r.AssociatedSpecs) == 0 {
		return "", nil
	}
	for _, sp := range r.AssociatedSpecs {
		if sp.Path != "" && !record.IsRecordFilePath(sp.Path) && !specPathOK(sp.Path) {
			return "", fmt.Errorf("%w: %q", ErrSpecPath, sp.Path)
		}
	}
	hashes, missing := r.SpecHashes(resolve)
	if len(missing) > 0 {
		return "", fmt.Errorf("%w: %s", ErrSpecMissing, strings.Join(missing, ", "))
	}
	list := make([]string, len(hashes))
	for i, h := range hashes {
		list[i] = *h
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (d *dirStore) Snapshot(id string, version int) ([]byte, error) {
	if !validID(id) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	vdir := filepath.Join(d.root, "provenance", ".versions", id)
	entries, err := os.ReadDir(vdir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("records: read versions of %s: %w", id, err)
	}
	var name string
	for _, e := range entries {
		n := e.Name()
		dash := strings.IndexByte(n, '-')
		if e.IsDir() || dash < 1 || !strings.HasSuffix(n, ".yml") {
			continue
		}
		if v, err := strconv.Atoi(n[:dash]); err != nil || v != version {
			continue
		}
		if name != "" {
			return nil, fmt.Errorf("records: %s has two snapshots for version %d", id, version)
		}
		name = n
	}
	if name == "" {
		return nil, fmt.Errorf("%w: %s v%d", ErrNotFound, id, version)
	}
	// Let provenance verify the snapshot (content hash, envelope vs file
	// name) before any raw bytes are handed out.
	if _, err := d.fs.Snapshot(id, version); err != nil {
		return nil, fmt.Errorf("records: snapshot %s v%d failed verification: %w", id, version, err)
	}
	return os.ReadFile(filepath.Join(vdir, name))
}

// Stale is true exactly when GenHash differs. A version bump with an
// unchanged GenHash is NOT stale.
//
// Stale never reads spec files: a deleted or unreadable spec does not make
// generation staleness unanswerable (specs verify output; they do not drive it).
// That holds for stores from OpenDir. Any other Store implementation is asked
// via Current, so its own Current must not fail on specs if Stale is to be
// spec-independent.
func Stale(s Store, p Pin) (bool, error) {
	var cur Pin
	var err error
	if d, ok := s.(*dirStore); ok {
		cur, _, err = d.currentGen(p.RecordID)
	} else {
		cur, err = s.Current(p.RecordID)
	}
	if err != nil {
		return false, err
	}
	return cur.GenHash != p.GenHash, nil
}

// ShapeStore is a Store that can write a record's shape. It is a separate
// interface so read-only Store implementations stay as they are.
type ShapeStore interface {
	Store
	// SetShape makes shape the record's shape. When it already is (compared
	// canonically) nothing is written and changed is false; otherwise the
	// change is a new record version through provenance's Commit (the one
	// sanctioned revise path), with the given reason and author.
	SetShape(id string, shape []any, reason, author string) (changed bool, err error)
}

func (d *dirStore) SetShape(id string, shape []any, reason, author string) (bool, error) {
	if !validID(id) {
		return false, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r, doc, err := d.fs.Load(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return false, fmt.Errorf("records: load %s: %w", id, err)
	}
	// Compare what gen_hash covers (normalised, entries ordered by path), so an
	// import that changes only CRLF or NFC form is not a new version.
	if record.GenCanonical(record.GenInput{Shape: r.Shape}) == record.GenCanonical(record.GenInput{Shape: shape}) {
		return false, nil
	}
	if reason == "" {
		return false, errors.New("records: a shape change needs a reason")
	}
	r.Shape = shape
	if _, err := record.Commit(d.fs, r, doc, record.Bump{Reason: reason, Author: author}); err != nil {
		return false, fmt.Errorf("records: revise %s: %w", id, err)
	}
	return true, nil
}

// SkeletonHash is the hash of the whole skeleton the planned records carry:
// the generator's name and every file entry of every shape, sorted by path.
// It covers content only, never record ids or versions, so a version bump
// that changes no shape does not move it. Records without a shape contribute
// nothing. It is pinned in every region's evidence in render mode (SPEC §7),
// because a region's code depends on declarations anywhere in the layout.
func SkeletonHash(generator string, pins []Pin) (string, error) {
	type entry struct {
		path string
		v    any
	}
	var es []entry
	for _, p := range pins {
		for _, e := range p.Shape {
			m, ok := e.(map[string]any)
			if !ok {
				return "", fmt.Errorf("records: %s: shape entry is not a mapping", p.RecordID)
			}
			path, _ := m["path"].(string)
			es = append(es, entry{path, e})
		}
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].path < es[j].path })
	files := make([]any, len(es))
	for i, e := range es {
		files[i] = e.v
	}
	c := record.Canonical(map[string]any{"generator": generator, "files": files})
	sum := sha256.Sum256([]byte(c))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
