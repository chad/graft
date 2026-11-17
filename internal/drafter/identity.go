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

// reviseThreshold: at least half of a draft's clauses must already belong to a
// record for the draft to be treated as that record's next version.
const reviseThreshold = 0.5

// Map proposes, for each draft, a revision of an accepted record, a new record,
// or a question. It never mints an id, and never lets two drafts revise one
// record (that is a split, which a human decides).
func Map(drafts []Draft, accepted []Accepted) []Proposal {
	out := make([]Proposal, len(drafts))
	claims := map[string][]int{}
	for i, d := range drafts {
		out[i] = mapOne(d, accepted)
		if out[i].Kind == Revise {
			claims[out[i].RecordID] = append(claims[out[i].RecordID], i)
		}
	}
	for id, idx := range claims {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			out[i] = Proposal{Kind: Question, Candidates: []string{id}, Draft: out[i].Draft,
				Reason: fmt.Sprintf("%d drafts derive from %s: is it being split?", len(idx), id)}
		}
	}
	return out
}

func mapOne(d Draft, accepted []Accepted) Proposal {
	if !d.Receipt.Validated {
		return Proposal{Kind: NoProposal, Draft: d, Reason: d.Receipt.Reason}
	}
	best, bestScore, tie := "", 0.0, false
	var anchorMatches []string
	for _, a := range accepted {
		s := overlap(d.DerivedFrom, a.DerivedFrom)
		switch {
		case s > bestScore:
			best, bestScore, tie = a.ID, s, false
		case s == bestScore && s > 0:
			tie = true
		}
		if a.Anchor == d.Anchor {
			anchorMatches = append(anchorMatches, a.ID)
		}
	}
	switch {
	case bestScore >= reviseThreshold && !tie:
		return Proposal{Kind: Revise, RecordID: best, Draft: d,
			Reason: fmt.Sprintf("%.0f%% of its clauses derive from %s", bestScore*100, best)}
	case bestScore > 0:
		cands := []string{best}
		if tie {
			cands = nil
			for _, a := range accepted {
				if overlap(d.DerivedFrom, a.DerivedFrom) == bestScore {
					cands = append(cands, a.ID)
				}
			}
		}
		return Proposal{Kind: Question, Candidates: cands, Draft: d,
			Reason: fmt.Sprintf("only %.0f%% overlap with an accepted record: a revision, or new?", bestScore*100)}
	case len(anchorMatches) > 0:
		slices.Sort(anchorMatches)
		return Proposal{Kind: Question, Candidates: anchorMatches, Draft: d,
			Reason: "same section as an accepted record but no shared clauses: rewritten, or new?"}
	}
	return Proposal{Kind: New, Draft: d, Reason: "no accepted record derives from these clauses"}
}

// overlap is the fraction of the draft's clauses already in the record.
func overlap(draft, record []string) float64 {
	if len(draft) == 0 {
		return 0
	}
	n := 0
	for _, h := range draft {
		if slices.Contains(record, h) {
			n++
		}
	}
	return float64(n) / float64(len(draft))
}
