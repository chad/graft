package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chad/graft/internal/crossref"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/recording"
	"github.com/livecodelife/provenance/record"
)

// Import is one statement copied into a record from another section by an
// accepted cross-reference. The source is the statement's section record and
// clause hash: while that clause is in the spec the copy stays, and when it is edited
// or removed the copy goes (the edited text is a new changed statement that
// the cross-reference step asks about again).
type Import struct {
	// FromRecord is the source section's record id: stable when the section is
	// renamed, unlike its heading. FromSection is the heading, for display.
	FromRecord  string `json:"from_record"`
	FromSection string `json:"from_section"`
	FromHash    string `json:"from_hash"`
	Type        string `json:"type"`
	Text        string `json:"text"`
	Reason      string `json:"reason"`
}

// ErrCrossref: some statement that is new or changed has no valid
// cross-reference answer, and the waiver was not given. Nothing is written.
var ErrCrossref = errors.New("pipeline: changed statements have not been cross-referenced")

// CrossrefOptions are the cross-reference inputs of one acceptance.
type CrossrefOptions struct {
	Proposal *crossref.Proposal // an accepted proposal, or nil
	Waive    bool               // --no-crossref: accept without any answer, loudly
	Report   *CrossrefReport    // filled in when non-nil
	// Skeleton is set in render mode: locations are checked against it.
	Skeleton *recording.Skeleton
	// Root is the code tree: with it, a location's identifier is checked at
	// accept too, not only when the proposal was made.
	Root string
}

// CrossrefReport says what the acceptance did about cross-references; the CLI
// prints it, so a dropped rule can no longer pass as "applied".
type CrossrefReport struct {
	Skipped  string   // why nothing was checked (first run), else ""
	Changed  int      // statements that are new or changed
	Accepted []string // "statement -> record" for each import made
	Local    []string // statements judged local, printed for a human to check
	Waived   int      // changed statements accepted unchecked (--no-crossref)
	Dropped  []string // imports removed because their source went
}

// Unchecked is the number of changed statements no answer covered.
func (r *CrossrefReport) Unchecked() int { return r.Waived }

// CrossrefState computes what the cross-reference step is asked about: the
// statements that are new or changed since the last accept (deterministic, no
// model), and every record they could constrain. It reads, never writes.
//
// skel, when non-nil (render mode), gives each record the stubs and decls of
// its skeleton file, which a cross-reference location must name.
func CrossrefState(repo string, drafts []drafter.Draft, skel *recording.Skeleton, opts AcceptOptions) ([]crossref.Stmt, []crossref.Rec, error) {
	idx, err := LoadIndex(repo)
	if err != nil {
		return nil, nil, err
	}
	props := drafter.Map(drafts, acceptedOf(idx))
	var undecided []string
	for _, p := range props {
		if p.Kind != drafter.New && p.Kind != drafter.Revise {
			undecided = append(undecided, fmt.Sprintf("%s: %s (%s)", p.Draft.Section, p.Kind, p.Reason))
		}
	}
	if len(undecided) > 0 {
		return nil, nil, fmt.Errorf("%w:\n  %s", ErrUndecided, strings.Join(undecided, "\n  "))
	}
	fs := &record.FilesStore{Root: repo}
	files, err := placeFiles(fs, idx, props, opts)
	if err != nil {
		return nil, nil, err
	}
	return crossrefState(fs, idx, props, skel, files)
}

// placeFiles is the file each proposal's record will own, exactly as accept
// will place it (a "files" config that moves a record is refused here too), so a
// cross-reference location is checked against the file that will exist.
func placeFiles(fs *record.FilesStore, idx *Index, props []drafter.Proposal, opts AcceptOptions) ([]string, error) {
	return place(props, idx, opts, opts.ext(), func(id string) ([]string, error) {
		r, _, err := fs.Load(id)
		if err != nil {
			return nil, fmt.Errorf("pipeline: load %s: %w", id, err)
		}
		return r.AffectedScope, nil
	})
}

func acceptedOf(idx *Index) []drafter.Accepted {
	var accepted []drafter.Accepted
	for id, e := range idx.Records {
		accepted = append(accepted, drafter.Accepted{ID: id, DerivedFrom: e.DerivedFrom, Anchor: e.Anchor})
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID < accepted[j].ID })
	return accepted
}

// importNote marks a statement a record imported from another section; the
// record's own text (what a cross-reference quote may cite) excludes it.
const importNote = `(cross-reference: stated in "`

func ownLines(text string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.Contains(l, importNote) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func crossrefState(fs *record.FilesStore, idx *Index, props []drafter.Proposal, skel *recording.Skeleton, files []string) ([]crossref.Stmt, []crossref.Rec, error) {
	if len(idx.Records) == 0 {
		return nil, nil, nil // the first accept: no other record exists to constrain
	}
	var stmts []crossref.Stmt
	var recs []crossref.Rec
	seen := map[string]bool{} // (section, hash): a sentence twice in one section is one statement
	add := func(section string, s drafter.Statement, own string) {
		k := section + "\x00" + s.ClauseHash
		if !seen[k] {
			seen[k] = true
			stmts = append(stmts, crossref.Stmt{Section: section, Hash: s.ClauseHash, Type: string(s.Type), Text: s.Text, Own: own})
		}
	}
	live := map[string]bool{} // record id + "\x00" + hash: the statements the spec now has, per existing record
	for i, p := range props {
		d := p.Draft
		have := map[string]bool{}
		own := ""
		if p.Kind == drafter.Revise {
			own = p.RecordID
			for _, h := range idx.Records[own].DerivedFrom {
				have[h] = true
			}
			for _, s := range d.Statements {
				live[own+"\x00"+s.ClauseHash] = true
			}
			r, _, err := fs.Load(own)
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: load %s: %w", own, err)
			}
			e := idx.Records[own]
			// a quote must cite the record's OWN statements, not text imported into it
			cons := make([]string, 0, len(r.Constraints))
			for _, c := range r.Constraints {
				if !strings.Contains(c, importNote) {
					cons = append(cons, c)
				}
			}
			rec := crossref.Rec{ID: own, Section: d.Section, File: files[i], Intent: ownLines(r.Intent), Constraints: cons}
			for _, im := range e.Imports {
				rec.Imports = append(rec.Imports, im.Text+` (from "`+im.FromSection+`")`)
			}
			recs = append(recs, withSymbols(rec, skel))
		} else {
			recs = append(recs, withSymbols(crossref.Rec{Section: d.Section, File: files[i]}, skel))
		}
		for _, s := range d.Statements {
			if !have[s.ClauseHash] {
				add(d.Section, s, own)
			}
		}
	}
	// Belt and braces: an import whose source statement is no longer in its
	// record is dropped at accept. If that sentence is still somewhere in the
	// spec it must be answered again, never silently lost, so it counts as changed.
	for _, p := range props {
		if p.Kind != drafter.Revise {
			continue
		}
		for _, im := range idx.Records[p.RecordID].Imports {
			if live[im.FromRecord+"\x00"+im.FromHash] {
				continue
			}
			for j, q := range props {
				own := ""
				if q.Kind == drafter.Revise {
					own = q.RecordID
				}
				_ = j
				for _, s := range q.Draft.Statements {
					if s.ClauseHash == im.FromHash {
						add(q.Draft.Section, s, own)
					}
				}
			}
		}
	}
	return stmts, recs, nil
}

// crossrefImports validates the proposal against the changed statements and
// returns, per proposal index, the imports to add. A nil opts leaves the
// library as it was: no check, no imports.
func crossrefImports(fs *record.FilesStore, idx *Index, props []drafter.Proposal, ids, files []string, opts *CrossrefOptions) (map[int][]Import, error) {
	if opts == nil {
		return nil, nil
	}
	rep := opts.Report
	if rep == nil {
		rep = &CrossrefReport{}
	}
	stmts, recs, err := crossrefState(fs, idx, props, opts.Skeleton, files)
	if err != nil {
		return nil, err
	}
	if len(idx.Records) == 0 {
		rep.Skipped = "first accept: no other record exists to constrain"
		return nil, nil
	}
	rep.Changed = len(stmts)
	if len(stmts) == 0 {
		return nil, nil
	}
	if opts.Waive {
		if opts.Proposal != nil {
			return nil, errors.New("pipeline: a crossref proposal and the --no-crossref waiver are exclusive")
		}
		rep.Waived = len(stmts)
		return nil, nil
	}
	var answers []crossref.Answer
	if opts.Proposal != nil {
		answers = opts.Proposal.Answers
	}
	checked, ok := crossref.Validate(stmts, recs, answers, opts.Root)
	if !ok {
		var bad []string
		for _, a := range checked {
			if a.Status != crossref.OK {
				bad = append(bad, fmt.Sprintf("%s: %q: %s", a.Status, a.Text, a.Detail))
			}
		}
		return nil, fmt.Errorf("%w (run graft crossref, review its proposal, and pass it with --crossref; or --no-crossref to accept unchecked):\n  %s",
			ErrCrossref, strings.Join(bad, "\n  "))
	}
	propOf := map[string]int{}
	for i, p := range props {
		if p.Kind == drafter.Revise {
			propOf["id:"+p.RecordID] = i
		} else {
			propOf["sec:"+p.Draft.Section] = i
		}
	}
	out := map[int][]Import{}
	for _, a := range checked {
		if a.Verdict == crossref.Local {
			rep.Local = append(rep.Local, fmt.Sprintf("%s: %q", a.Section, a.Text))
			continue
		}
		typ, from := "", ""
		for _, s := range stmts {
			if s.Hash == a.Hash && s.Section == a.Section {
				typ = s.Type
				from = s.Own
			}
		}
		if from == "" { // the source section is new in this change: its record id is the one accept mints for it
			for i, p := range props {
				if p.Kind == drafter.New && p.Draft.Section == a.Section {
					from = ids[i]
				}
			}
		}
		for _, t := range a.Targets {
			r := crossref.Resolve(recs, t.Record)
			key := "sec:" + r.Section
			if r.ID != "" {
				key = "id:" + r.ID
			}
			i, found := propOf[key]
			if !found {
				return nil, fmt.Errorf("%w: target %q is not part of this spec", ErrCrossref, t.Record)
			}
			out[i] = append(out[i], Import{FromRecord: from, FromSection: a.Section, FromHash: a.Hash, Type: typ, Text: a.Text, Reason: t.Reason})
			rep.Accepted = append(rep.Accepted, fmt.Sprintf("%q (%s) -> %s", a.Text, a.Section, r.Section))
		}
	}
	return out, nil
}

// mergeImports keeps the previous imports whose source statement is still in
// its source RECORD (so renaming a section's heading keeps them), drops the
// rest, and adds the new ones (replacing a kept one with the same source).
func mergeImports(prev, add []Import, props []drafter.Proposal, ids []string) (kept, dropped []Import, added bool) {
	live := map[string]bool{}
	heading := map[string]string{}
	for i, p := range props {
		heading[ids[i]] = p.Draft.Section
		for _, s := range p.Draft.Statements {
			live[ids[i]+"\x00"+s.ClauseHash] = true
		}
	}
	for _, im := range prev {
		if live[im.FromRecord+"\x00"+im.FromHash] {
			im.FromSection = heading[im.FromRecord] // follows a rename
			kept = append(kept, im)
		} else {
			dropped = append(dropped, im)
		}
	}
	for _, im := range add {
		dup := false
		for _, k := range kept {
			if k.FromRecord == im.FromRecord && k.FromHash == im.FromHash {
				dup = true
			}
		}
		if !dup {
			kept = append(kept, im)
			added = true
		}
	}
	return kept, dropped, added
}

// withImports appends the imported statements to a draft's own, marked as
// cross-references, so the record's intent and the model's prompt carry them.
func withImports(own []drafter.Statement, imps []Import) []drafter.Statement {
	out := append([]drafter.Statement(nil), own...)
	for _, im := range imps {
		out = append(out, drafter.Statement{Type: drafter.Type(im.Type), Text: im.Text + ` (cross-reference: stated in "` + im.FromSection + `"; it binds this section's code too)`, ClauseHash: im.FromHash})
	}
	return out
}

// CrossrefInput is one run of the cross-reference step.
type CrossrefInput struct {
	Repo        string
	Drafts      []drafter.Draft
	Model       model.Model
	Conventions string
	Root        string // code tree for location checks; "" skips them
	WithCode    bool   // show each record's current file to the model
	Retries     int
	Strict      bool
	// Accept is how accept will place the records' files (language extension,
	// "files" config, root): locations are checked against those files.
	Accept   AcceptOptions
	Skeleton *recording.Skeleton // render mode: a location must name a stub or decl of the target's skeleton file
}

// withSymbols marks rec as a render-mode record and lists the stubs and decls
// of its skeleton file (matched by path, record id or section).
func withSymbols(rec crossref.Rec, skel *recording.Skeleton) crossref.Rec {
	if skel == nil {
		return rec
	}
	rec.Render = true
	for _, f := range skel.Files {
		if !(rec.File != "" && f.Path == rec.File) && !(rec.ID != "" && f.Record == rec.ID) && !(f.Section != "" && f.Section == rec.Section) {
			continue
		}
		add := func(name, recv string) {
			rec.Symbols = append(rec.Symbols, name)
			if recv = strings.TrimPrefix(recv, "*"); recv != "" {
				rec.Symbols = append(rec.Symbols, recv+"."+name)
			}
		}
		for _, d := range f.Decls {
			add(d.Name, d.Receiver)
		}
		for _, st := range f.Stubs {
			add(st.Name, st.ReceiverType)
		}
	}
	return rec
}

// Crossref runs the step: it finds the changed statements, asks the model once
// and returns the validated proposal, the statements and the records (for
// warnings) and the number of model calls. With nothing changed it makes no call.
func Crossref(ctx context.Context, in CrossrefInput) (crossref.Proposal, []crossref.Stmt, []crossref.Rec, int, error) {
	stmts, recs, err := CrossrefState(in.Repo, in.Drafts, in.Skeleton, in.Accept)
	if err != nil || len(stmts) == 0 {
		return crossref.Proposal{Schema: crossref.SchemaVersion}, stmts, recs, 0, err
	}
	ci := crossref.Input{Stmts: stmts, Recs: recs, Conventions: in.Conventions, Root: in.Root, Retries: in.Retries, Strict: in.Strict}
	if in.WithCode && in.Root != "" {
		ci.Code = map[string]string{}
		for _, r := range recs {
			if b, err := readTreeFile(in.Root, r.File); err == nil {
				ci.Code[r.File] = b
			}
		}
	}
	p, calls, err := crossref.Propose(ctx, in.Model, ci)
	return p, stmts, recs, calls, err
}

func readTreeFile(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("no file")
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	return string(b), err
}
