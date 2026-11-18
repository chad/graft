// Package records is graft's view of provenance records: stable pins and
// staleness. Schema, canonical hashing and gen_hash come from
// github.com/livecodelife/provenance/record; nothing is reimplemented here.
package records

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/livecodelife/provenance/record"
)

// ErrNotFound means the record, or the requested version, does not exist.
var ErrNotFound = errors.New("records: not found")

// Pin identifies a record version and the generation-driving content it had.
type Pin struct {
	RecordID string // stable id, never derived from content
	Version  int
	GenHash  string // hash over generation-driving fields only
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
	if !validID(id) {
		return Pin{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r, _, err := d.fs.Load(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Pin{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Pin{}, fmt.Errorf("records: load %s: %w", id, err)
	}
	if r.ID != id {
		return Pin{}, fmt.Errorf("records: %s.yml holds record %q", id, r.ID)
	}
	v := r.Version
	if v == 0 {
		v = 1 // a v1 record reads as version 1
	}
	return Pin{RecordID: r.ID, Version: v, GenHash: r.GenHash()}, nil
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
func Stale(s Store, p Pin) (bool, error) {
	cur, err := s.Current(p.RecordID)
	if err != nil {
		return false, err
	}
	return cur.GenHash != p.GenHash, nil
}
