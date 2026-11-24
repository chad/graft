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
	// Imports are statements copied in from other sections by an accepted
	// cross-reference (SPEC §12). They are not DerivedFrom, so they never
	// affect how a re-run maps a section onto this record; each follows its
	// source clause and is dropped when the source is edited or removed.
	Imports []Import `json:"imports,omitempty"`
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
	// Files places a new section's region in a chosen file (relative to the
	// root, forward slashes), e.g. "http/dial_stats.go" in an existing repo
	// whose packages live in directories. A key is a section's full title,
	// including the top heading ("wtf › API › Dial stats"), or, when
	// unambiguous, its last part only ("Dial stats"); a middle form
	// ("API › Dial stats") or a key naming no section is an error. Sections
	// not named get fileFor's name in the root.
	// A record keeps its file once accepted: naming a different file for it
	// later is an error, not a silent no-op.
	Files map[string]string
	// Root is the code tree. When set, a new record's file must not exist
	// yet (a section never takes over a hand-written file) and its path must
	// not pass through a symlink.
	Root string
	// Crossref, when non-nil, enforces the cross-reference rule (SPEC §12):
	// every statement that is new or changed since the last accept needs an
	// answer, or the acceptance is refused with ErrCrossref. Nil leaves the
	// library behaviour as it was; `graft run --accept` always sets it.
	Crossref *CrossrefOptions
}

// ErrBadFile: a section file that cannot be used.
var ErrBadFile = errors.New("pipeline: bad section file")

func (o AcceptOptions) ext() string {
	if o.Ext == "" {
		return ".go"
	}
	return o.Ext
}

// lastPart is a section title's last " › " part.
func lastPart(section string) string {
	parts := strings.Split(section, " › ")
	return parts[len(parts)-1]
}

// configured resolves every Files key to exactly one section: a full title
// first, else a last part matching exactly one section.
func (o AcceptOptions) configured(sections []string) (map[string]string, error) {
	out := map[string]string{}
	full := map[string]bool{}
	byLast := map[string][]string{}
	for _, s := range sections {
		full[s] = true
		byLast[lastPart(s)] = append(byLast[lastPart(s)], s)
	}
	keys := make([]string, 0, len(o.Files))
	for k := range o.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sec := ""
		switch {
		case full[k]:
			sec = k
		case len(byLast[k]) == 1:
			sec = byLast[k][0]
		case len(byLast[k]) > 1:
			return nil, fmt.Errorf("%w: key %q matches several sections (%s); use the full title", ErrBadFile, k, strings.Join(byLast[k], ", "))
		default:
			return nil, fmt.Errorf("%w: key %q names no section of the spec", ErrBadFile, k)
		}
		if _, dup := out[sec]; dup {
			return nil, fmt.Errorf("%w: section %q is named twice in files", ErrBadFile, sec)
		}
		out[sec] = o.Files[k]
	}
	return out, nil
}

// CheckConfigured checks the Files keys and paths against a spec's drafts,
// without the records or the tree: what a dry run can say.
func (o AcceptOptions) CheckConfigured(drafts []drafter.Draft) error {
	sections := make([]string, len(drafts))
	for i, d := range drafts {
		sections[i] = d.Section
	}
	conf, err := o.configured(sections)
	if err != nil {
		return err
	}
	for _, f := range conf {
		if err := o.checkPath(f); err != nil {
			return err
		}
	}
	return nil
}

// checkPath: clean, relative (forward slashes only), inside the tree, the
// language's extension, a real file name, and no hidden component.
func (o AcceptOptions) checkPath(f string) error {
	clean := path.Clean(f)
	bad := f == "" || strings.Contains(f, "\\") || clean != f || path.IsAbs(f) || clean == ".." || strings.HasPrefix(clean, "../") ||
		!strings.HasSuffix(f, o.ext()) || path.Base(f) == o.ext()
	for _, c := range strings.Split(clean, "/") {
		if strings.HasPrefix(c, ".") {
			bad = true
		}
	}
	if bad {
		return fmt.Errorf("%w: %q must be a clean relative path to a %s file, with no hidden component", ErrBadFile, f, o.ext())
	}
	return nil
}

// checkFresh: under Root, a new record's file must not exist yet and its
// path must not pass through a symlink.
func (o AcceptOptions) checkFresh(f string) error {
	if o.Root == "" {
		return nil
	}
	cur := o.Root
	for _, c := range strings.Split(f, "/") {
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q passes through a symlink", ErrBadFile, f)
		}
	}
	return fmt.Errorf("%w: %q already exists; a new section never takes over an existing (hand-written) file", ErrBadFile, f)
}

// place decides every draft's file before anything is written: an accepted
// record keeps its file (a configured different one is an error); a new one
// takes its configured file or fileFor's name, which must be valid, fresh
// under Root, and not shared with any other section, accepted or new.
//
// An accepted record's file is its affected_scope: the record, not the
// index or the config, is the source of truth. The index is a cache of it and
// must agree; "files" in graft.json only seeds a new record's scope.
func place(props []drafter.Proposal, idx *Index, opts AcceptOptions, ext string, scopeOf func(id string) ([]string, error)) ([]string, error) {
	sections := make([]string, len(props))
	for i, p := range props {
		sections[i] = p.Draft.Section
	}
	conf, err := opts.configured(sections)
	if err != nil {
		return nil, err
	}
	// file -> section, across accepted records (by their records) and this spec
	owner := map[string]string{}
	recordFile := func(id string) (string, error) {
		scope, err := scopeOf(id)
		if err != nil {
			return "", err
		}
		if len(scope) != 1 {
			return "", fmt.Errorf("%w: record %s has affected_scope %v; graft places each record in exactly one file", ErrBadFile, id, scope)
		}
		return scope[0], nil
	}
	for id, e := range idx.Records {
		f, err := recordFile(id)
		if err != nil {
			return nil, err
		}
		owner[f] = e.Section + " (" + id + ")"
	}
	files := make([]string, len(props))
	for i, p := range props {
		sec := p.Draft.Section
		want, named := conf[sec]
		if p.Kind != drafter.New {
			if e, ok := idx.Records[p.RecordID]; ok {
				file, err := recordFile(p.RecordID)
				if err != nil {
					return nil, err
				}
				if file != e.File {
					return nil, fmt.Errorf("%w: record %s places %q in %s but the index says %s; the record is the source of truth: fix or rebuild .graft/accepted.json", ErrBadFile, p.RecordID, sec, file, e.File)
				}
				if named && want != file {
					return nil, fmt.Errorf("%w: %q is already placed in %s (its record's affected_scope); moving a section is not supported (files names %s)", ErrBadFile, sec, file, want)
				}
				files[i] = file
				continue
			}
		}
		f := fileFor(sec, ext)
		if named {
			f = want
		}
		if err := opts.checkPath(f); err != nil {
			return nil, err
		}
		if other, ok := owner[f]; ok {
			hint := ""
			if !named {
				hint = "; place one of them with \"files\" in graft.json, keyed by its full title"
			}
			return nil, fmt.Errorf("%w: %q for %q is already %s's file%s", ErrBadFile, f, sec, other, hint)
		}
		if err := opts.checkFresh(f); err != nil {
			return nil, err
		}
		owner[f] = sec
		files[i] = f
	}
	return files, nil
}

// AcceptWith is Accept with per-tree options.
func AcceptWith(repo string, drafts []drafter.Draft, author string, now time.Time, opts AcceptOptions) ([]Accepted, error) {
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
	fs0 := &record.FilesStore{Root: repo}
	files, err := place(props, idx, opts, ext, func(id string) ([]string, error) {
		r, _, err := fs0.Load(id)
		if err != nil {
			return nil, fmt.Errorf("pipeline: load %s: %w", id, err)
		}
		return r.AffectedScope, nil
	})
	if err != nil {
		return nil, err
	}
	fs := &record.FilesStore{Root: repo}
	imports, err := crossrefImports(fs, idx, props, opts.Crossref)
	if err != nil {
		return nil, err
	}
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
		file := files[i]
		var prev []Import
		if p.Kind == drafter.Revise {
			prev = idx.Records[id].Imports
		}
		kept, dropped, added := mergeImports(prev, imports[i], drafts)
		if opts.Crossref != nil && opts.Crossref.Report != nil {
			for _, im := range dropped {
				opts.Crossref.Report.Dropped = append(opts.Crossref.Report.Dropped,
					fmt.Sprintf("%q (from %s) in %s: its source was edited or removed", im.Text, im.FromSection, d.Section))
			}
		}
		d.Statements = withImports(d.Statements, kept)
		r.Title = d.Section
		r.Intent, r.Constraints = intent(d)
		r.AffectedScope = []string{file}
		after, _ := r.ContentHash(fs.Specs())
		if created || after != before { // an unchanged re-run must not mint a version
			bump := record.Bump{Author: author, Now: now}
			if !created {
				bump.Reason = "spec revised"
				if added || len(dropped) > 0 {
					bump.Reason = "spec revised (cross-reference imports changed)"
				}
			}
			if _, err := record.Commit(fs, r, doc, bump); err != nil {
				return nil, fmt.Errorf("pipeline: commit %s: %w", id, err)
			}
		}
		e := Entry{Section: d.Section, File: file, DerivedFrom: d.DerivedFrom, Anchor: d.Anchor, Order: i, Imports: kept}
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

// SectionFile is the file a section's record owns under --render, where no
// "files" config places it: the skeleton must name exactly this path for the
// section (a record's affected_scope is that one file). `graft draft
// --skeleton` tells the model so.
func SectionFile(section, ext string) string { return fileFor(section, ext) }

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
