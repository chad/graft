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
	"github.com/livecodelife/provenance/record"
)

// Import is one statement copied into a record from another section by an
// accepted cross-reference. The source is the statement's section and clause
// hash: while that clause is in the spec the copy stays, and when it is edited
// or removed the copy goes (the edited text is a new changed statement that
// the cross-reference step asks about again).
type Import struct {
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
func CrossrefState(repo string, drafts []drafter.Draft) ([]crossref.Stmt, []crossref.Rec, error) {
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
	return crossrefState(&record.FilesStore{Root: repo}, idx, props)
}

func acceptedOf(idx *Index) []drafter.Accepted {
	var accepted []drafter.Accepted
	for id, e := range idx.Records {
		accepted = append(accepted, drafter.Accepted{ID: id, DerivedFrom: e.DerivedFrom, Anchor: e.Anchor})
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID < accepted[j].ID })
	return accepted
}

func crossrefState(fs *record.FilesStore, idx *Index, props []drafter.Proposal) ([]crossref.Stmt, []crossref.Rec, error) {
	if len(idx.Records) == 0 {
		return nil, nil, nil // the first accept: no other record exists to constrain
	}
	var stmts []crossref.Stmt
	var recs []crossref.Rec
	for _, p := range props {
		d := p.Draft
		have := map[string]bool{}
		own := ""
		if p.Kind == drafter.Revise {
			own = p.RecordID
			for _, h := range idx.Records[own].DerivedFrom {
				have[h] = true
			}
			r, _, err := fs.Load(own)
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: load %s: %w", own, err)
			}
			e := idx.Records[own]
			rec := crossref.Rec{ID: own, Section: e.Section, File: e.File, Intent: r.Intent, Constraints: r.Constraints}
			for _, im := range e.Imports {
				rec.Imports = append(rec.Imports, im.Text+` (from "`+im.FromSection+`")`)
			}
			recs = append(recs, rec)
		} else {
			recs = append(recs, crossref.Rec{Section: d.Section})
		}
		for _, s := range d.Statements {
			if !have[s.ClauseHash] {
				stmts = append(stmts, crossref.Stmt{Section: d.Section, Hash: s.ClauseHash, Type: string(s.Type), Text: s.Text, Own: own})
			}
		}
	}
	return stmts, recs, nil
}

// crossrefImports validates the proposal against the changed statements and
// returns, per proposal index, the imports to add. A nil opts leaves the
// library as it was: no check, no imports.
func crossrefImports(fs *record.FilesStore, idx *Index, props []drafter.Proposal, opts *CrossrefOptions) (map[int][]Import, error) {
	if opts == nil {
		return nil, nil
	}
	rep := opts.Report
	if rep == nil {
		rep = &CrossrefReport{}
	}
	stmts, recs, err := crossrefState(fs, idx, props)
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
	checked, ok := crossref.Validate(stmts, recs, answers, "")
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
		typ := ""
		for _, s := range stmts {
			if s.Hash == a.Hash {
				typ = s.Type
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
			out[i] = append(out[i], Import{FromSection: a.Section, FromHash: a.Hash, Type: typ, Text: a.Text, Reason: t.Reason})
			rep.Accepted = append(rep.Accepted, fmt.Sprintf("%q (%s) -> %s", a.Text, a.Section, r.Section))
		}
	}
	return out, nil
}

// mergeImports keeps the previous imports whose source clause is still in the
// spec, drops the rest, and adds the new ones (replacing a kept one with the
// same source).
func mergeImports(prev, add []Import, drafts []drafter.Draft) (kept, dropped []Import, added bool) {
	live := map[string]bool{}
	for _, d := range drafts {
		for _, s := range d.Statements {
			live[d.Section+"\x00"+s.ClauseHash] = true
		}
	}
	for _, im := range prev {
		if live[im.FromSection+"\x00"+im.FromHash] {
			kept = append(kept, im)
		} else {
			dropped = append(dropped, im)
		}
	}
	for _, im := range add {
		dup := false
		for _, k := range kept {
			if k.FromSection == im.FromSection && k.FromHash == im.FromHash {
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
}

// Crossref runs the step: it finds the changed statements, asks the model once
// and returns the validated proposal, the statements and the records (for
// warnings) and the number of model calls. With nothing changed it makes no call.
func Crossref(ctx context.Context, in CrossrefInput) (crossref.Proposal, []crossref.Stmt, []crossref.Rec, int, error) {
	stmts, recs, err := CrossrefState(in.Repo, in.Drafts)
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
