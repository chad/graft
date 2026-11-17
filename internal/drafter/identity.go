package drafter

import (
	"fmt"
	"slices"
)

// Accepted is an already-accepted record, as the drafter sees it: its stored
// id and the clause hashes it was derived from at acceptance.
type Accepted struct {
	ID          string
	DerivedFrom []string
	Anchor      string
}

// Kind is what a proposal asks a human (or reviewer) to do.
type Kind string

const (
	Revise     Kind = "revise"   // a new version of RecordID
	New        Kind = "new"      // a new record; its id is assigned at acceptance
	Question   Kind = "question" // the drafter cannot decide: new, or split, or which?
	NoProposal Kind = "no-confident-proposal"
)

// Proposal pairs a draft with what it proposes. It is never applied by the drafter.
type Proposal struct {
	Kind       Kind
	RecordID   string   // for Revise
	Candidates []string // for Question
	Draft      Draft
	Reason     string
}

// reviseThreshold: a draft is a record's next version only when at least half
// of the draft's clauses come from the record AND at least half of the
// record's clauses survive into the draft. One-sided overlap is a question.
const reviseThreshold = 0.5

// Map proposes, for each draft, a revision of an accepted record, a new record,
// or a question. It never mints an id. It recomputes every receipt rather
// than trusting the flag. Any record that more than one draft draws clauses
// from is a split, whatever the overlap, and a human decides it.
func Map(drafts []Draft, accepted []Accepted) []Proposal {
	out := make([]Proposal, len(drafts))
	claims := map[string][]int{}
	for i, d := range drafts {
		r := receipt(d)
		if !r.Validated || r.Digest != d.Receipt.Digest {
			reason := r.Reason
			if r.Validated {
				reason = "the draft does not match its receipt (edited after drafting)"
			}
			out[i] = Proposal{Kind: NoProposal, Draft: d, Reason: reason}
			continue
		}
		for _, a := range accepted {
			if overlap(d.DerivedFrom, a.DerivedFrom) > 0 {
				claims[a.ID] = append(claims[a.ID], i)
			}
		}
		out[i] = mapOne(d, accepted)
	}
	for id, idx := range claims {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			out[i] = Proposal{Kind: Question, Candidates: []string{id}, Draft: out[i].Draft,
				Reason: fmt.Sprintf("%d drafts draw clauses from %s: is it being split?", len(idx), id)}
		}
	}
	return out
}

func mapOne(d Draft, accepted []Accepted) Proposal {
	type scored struct {
		id                string
		ofDraft, ofRecord float64
	}
	var best []scored
	var anchorMatches []string
	for _, a := range accepted {
		s := scored{a.ID, overlap(d.DerivedFrom, a.DerivedFrom), overlap(a.DerivedFrom, d.DerivedFrom)}
		switch {
		case s.ofDraft <= 0:
		case len(best) == 0 || s.ofDraft > best[0].ofDraft:
			best = []scored{s}
		case s.ofDraft == best[0].ofDraft:
			best = append(best, s)
		}
		if a.Anchor == d.Anchor {
			anchorMatches = append(anchorMatches, a.ID)
		}
	}
	switch {
	case len(best) > 1:
		var ids []string
		for _, b := range best {
			ids = append(ids, b.id)
		}
		slices.Sort(ids)
		return Proposal{Kind: Question, Candidates: ids, Draft: d,
			Reason: fmt.Sprintf("ties at %.0f%% overlap between %v: which record is this?", best[0].ofDraft*100, ids)}
	case len(best) == 1 && best[0].ofDraft >= reviseThreshold && best[0].ofRecord >= reviseThreshold:
		b := best[0]
		return Proposal{Kind: Revise, RecordID: b.id, Draft: d,
			Reason: fmt.Sprintf("%.0f%% of its clauses derive from %s, and %.0f%% of %s survives", b.ofDraft*100, b.id, b.ofRecord*100, b.id)}
	case len(best) == 1:
		b := best[0]
		return Proposal{Kind: Question, Candidates: []string{b.id}, Draft: d,
			Reason: fmt.Sprintf("%.0f%% of its clauses derive from %s, but only %.0f%% of %s survives: a revision, or new?", b.ofDraft*100, b.id, b.ofRecord*100, b.id)}
	case len(anchorMatches) > 0:
		slices.Sort(anchorMatches)
		return Proposal{Kind: Question, Candidates: anchorMatches, Draft: d,
			Reason: "same section as an accepted record but no shared clauses: rewritten, or new?"}
	}
	return Proposal{Kind: New, Draft: d, Reason: "no accepted record derives from these clauses"}
}

// overlap is the fraction of a's hashes that are also in b.
func overlap(a, b []string) float64 {
	if len(a) == 0 {
		return 0
	}
	n := 0
	for _, h := range a {
		if slices.Contains(b, h) {
			n++
		}
	}
	return float64(n) / float64(len(a))
}
