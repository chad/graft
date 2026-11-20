// Package pipeline is `graft run`: spec → drafts → accepted records → one
// convergence region per record. Sedum rendering slots in under convergence
// once records → recordings lands; until then every region is synthesized
// and gated.
package pipeline

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chad/graft/internal/drafter"
	"github.com/livecodelife/provenance/record"
)

// Entry is graft's side of an accepted record: what the drafter needs to map
// a re-run back onto it (SPEC §8: identity assigned at acceptance, stored).
type Entry struct {
	Section     string   `json:"section"`
	File        string   `json:"file"`
	DerivedFrom []string `json:"derived_from"`
	Anchor      string   `json:"anchor"`
	Order       int      `json:"order"` // position in the spec, for generation order
}

// Index is .graft/accepted.json in the records repo.
type Index struct {
	Records map[string]Entry `json:"records"`
}

func indexPath(repo string) string { return filepath.Join(repo, ".graft", "accepted.json") }

// LoadIndex reads the index; a missing one is empty.
func LoadIndex(repo string) (*Index, error) {
	idx := &Index{Records: map[string]Entry{}}
	b, err := os.ReadFile(indexPath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, idx); err != nil {
		return nil, fmt.Errorf("pipeline: %s: %w", indexPath(repo), err)
	}
	if idx.Records == nil {
		idx.Records = map[string]Entry{}
	}
	return idx, nil
}

func (idx *Index) save(repo string) error {
	if err := os.MkdirAll(filepath.Dir(indexPath(repo)), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(idx, "", "  ")
	tmp := indexPath(repo) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath(repo))
}

// Accepted is one record ready to generate.
type Accepted struct {
	ID      string
	Entry   Entry
	Draft   drafter.Draft
	Created bool // true when this acceptance created the record
}

// ErrUndecided: the drafter could not decide for some drafts (questions or no
// confident proposal). A human resolves them; acceptance applies nothing.
var ErrUndecided = errors.New("pipeline: some drafts need a human decision")

// Accept maps drafts onto the index and, only if every draft is a clean New or
// Revise, writes them as provenance records (imprints owning one file each)
// through provenance's own Commit, so every version is snapshotted. Accepting
// is the human step in SPEC §8; `graft run --accept` is that human saying yes.
func Accept(repo string, drafts []drafter.Draft, author string, now time.Time) ([]Accepted, error) {
	return AcceptExt(repo, drafts, author, now, ".go")
}

// AcceptExt is Accept for a target language whose source files end in ext.
func AcceptExt(repo string, drafts []drafter.Draft, author string, now time.Time, ext string) ([]Accepted, error) {
	return AcceptWith(repo, drafts, author, now, AcceptOptions{Ext: ext})
}

// AcceptOptions are the per-tree choices Accept makes for new records.
type AcceptOptions struct {
	Ext string // source file extension (".go" when empty)
	// Files places a section's region in a chosen file (relative to the
	// root), e.g. "http/dial_stats.go" in an existing repo whose packages
	// live in directories. Keys are a section title, full ("wtf › Dial stats")
	// or its last part ("Dial stats"). Sections not named get fileFor's name
	// in the root. A record keeps its file once accepted.
	Files map[string]string
}

// ErrBadFile: a configured section file that cannot be used.
var ErrBadFile = errors.New("pipeline: bad section file")

// fileOf is the configured file for a section, if any.
func (o AcceptOptions) fileOf(section string) (string, bool) {
	if f, ok := o.Files[section]; ok {
		return f, true
	}
	parts := strings.Split(section, " › ")
	f, ok := o.Files[parts[len(parts)-1]]
	return f, ok
}

// CheckFiles validates configured section files: clean, relative, inside the
// root, with the language's extension, and no two sections in one file.
func (o AcceptOptions) CheckFiles() error {
	seen := map[string]string{}
	for sec, f := range o.Files {
		clean := path.Clean(f)
		if f == "" || clean != f || path.IsAbs(f) || clean == ".." || strings.HasPrefix(clean, "../") || !strings.HasSuffix(f, o.ext()) {
			return fmt.Errorf("%w: %q for %q must be a clean relative path ending in %s", ErrBadFile, f, sec, o.ext())
		}
		if other, ok := seen[f]; ok {
			return fmt.Errorf("%w: %q is given to both %q and %q", ErrBadFile, f, other, sec)
		}
		seen[f] = sec
	}
	return nil
}

func (o AcceptOptions) ext() string {
	if o.Ext == "" {
		return ".go"
	}
	return o.Ext
}

// AcceptWith is Accept with per-tree options.
func AcceptWith(repo string, drafts []drafter.Draft, author string, now time.Time, opts AcceptOptions) ([]Accepted, error) {
	if err := opts.CheckFiles(); err != nil {
		return nil, err
	}
	ext := opts.ext()
	idx, err := LoadIndex(repo)
	if err != nil {
		return nil, err
	}
	var accepted []drafter.Accepted
	for id, e := range idx.Records {
		accepted = append(accepted, drafter.Accepted{ID: id, DerivedFrom: e.DerivedFrom, Anchor: e.Anchor})
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID < accepted[j].ID })
	props := drafter.Map(drafts, accepted)
	var undecided []string
	for _, p := range props {
		if p.Kind != drafter.New && p.Kind != drafter.Revise {
			undecided = append(undecided, fmt.Sprintf("%s: %s (%s)", p.Draft.Section, p.Kind, p.Reason))
		}
	}
	if len(undecided) > 0 {
		return nil, fmt.Errorf("%w:\n  %s", ErrUndecided, strings.Join(undecided, "\n  "))
	}
	fs := &record.FilesStore{Root: repo}
	var out []Accepted
	for i, p := range props {
		d := p.Draft
		id, created := p.RecordID, false
		var r *record.Record
		var doc *record.Document
		if p.Kind == drafter.New {
			id, created = newID(now), true
			r = &record.Record{Schema: 2, ID: id, Type: record.TypeImprint, Status: record.StatusOpen,
				CreatedAt: now.UTC().Format("2006-01-02"), Author: author}
		} else if r, doc, err = fs.Load(id); err != nil {
			return nil, fmt.Errorf("pipeline: load %s: %w", id, err)
		}
		before := ""
		if !created {
			before, _ = r.ContentHash(fs.Specs())
		}
		file := fileFor(d.Section, ext)
		if f, ok := opts.fileOf(d.Section); ok {
			file = f
		}
		if e, ok := idx.Records[id]; ok {
			file = e.File // a record keeps its file
		}
		r.Title = d.Section
		r.Intent, r.Constraints = intent(d)
		r.AffectedScope = []string{file}
		after, _ := r.ContentHash(fs.Specs())
		if created || after != before { // an unchanged re-run must not mint a version
			bump := record.Bump{Author: author, Now: now}
			if !created {
				bump.Reason = "spec revised"
			}
			if _, err := record.Commit(fs, r, doc, bump); err != nil {
				return nil, fmt.Errorf("pipeline: commit %s: %w", id, err)
			}
		}
		e := Entry{Section: d.Section, File: file, DerivedFrom: d.DerivedFrom, Anchor: d.Anchor, Order: i}
		idx.Records[id] = e
		// Save after every record: a failure part-way leaves the index naming
		// exactly the records that were written, so a re-run revises them
		// instead of minting duplicates.
		if err := idx.save(repo); err != nil {
			return nil, err
		}
		out = append(out, Accepted{ID: id, Entry: e, Draft: d, Created: created})
	}
	return out, nil
}

func newID(now time.Time) string {
	var b [4]byte
	rand.Read(b[:])
	return fmt.Sprintf("prov-%d-%s", now.UTC().Year(), hex.EncodeToString(b[:]))
}

// fileFor names the source file (with extension ext) a section's region lives in.
func fileFor(section, ext string) string {
	parts := strings.Split(section, " › ")
	last := strings.ToLower(parts[len(parts)-1])
	var b strings.Builder
	for _, r := range last {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "section"
	}
	return name + ext
}

// intent renders a draft's statements as the record's intent; constraints and
// invariants are also listed as constraints.
func intent(d drafter.Draft) (string, []string) {
	var lines, cons []string
	for _, s := range d.Statements {
		lines = append(lines, fmt.Sprintf("- [%s] %s", s.Type, s.Text))
		if s.Type == drafter.Constraint || s.Type == drafter.Invariant {
			cons = append(cons, s.Text)
		}
	}
	return d.Section + "\n" + strings.Join(lines, "\n"), cons
}
