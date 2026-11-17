package drafter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Statement is one typed statement in a draft.
type Statement struct {
	Type       Type
	Text       string
	Confidence float64
	ClauseHash string
	Line       int
	Ambiguous  bool // the rubric's top two types tied
}

// Receipt is the evidence that a draft validates (SPEC §8). Map never trusts
// a draft's stored fields: it re-derives clause hashes, DerivedFrom and Anchor
// from the statements' text and section, then recomputes the receipt.
// A draft validates when:
//   - re-extracting its rendering yields exactly its statements: the same
//     clause hashes, no more, no fewer (lossless round trip);
//   - every statement re-extracts to the type it was drafted with; and
//   - no statement is ambiguous: the rubric's top two types did not tie.
//
// A draft that fails is "no confident proposal" and is not shown as a fix.
type Receipt struct {
	Validated bool
	Reason    string
}

// Draft is a proposed record. It has no id: identity is assigned at acceptance.
type Draft struct {
	Section     string      // heading path, " › "-joined
	Tier        string      // "blueprint": the prose drafter never emits imprints
	Statements  []Statement // everything but CONTEXT
	Context     []Statement // kept and reported, never silently dropped
	DerivedFrom []string    // clause hashes of Statements, sorted
	Anchor      string      // candidate matching key only — never an identity
	Receipt     Receipt
}

// Drafts groups a spec's clauses by section into draft blueprints.
func Drafts(markdown string) []Draft {
	var (
		order   []string
		by      = map[string]*Draft{}
		seen    = map[string]int{}
		lastSec string
		lastKey string
	)
	for _, c := range Segment(markdown) {
		sec := sectionKey(c.Section)
		key := lastKey
		if sec != lastSec {
			// A heading path that recurs later in the spec is a different
			// section with the same name, not a continuation of the first.
			seen[sec]++
			key = sec
			if seen[sec] > 1 {
				key = fmt.Sprintf("%s (%d)", sec, seen[sec])
			}
			lastSec, lastKey = sec, key
		}
		d, ok := by[key]
		if !ok {
			d = &Draft{Section: key, Tier: "blueprint"}
			by[key] = d
			order = append(order, key)
		}
		t, conf, tied := classify(c.Text, c.FromList)
		st := Statement{Type: t, Text: c.Text, Confidence: conf, ClauseHash: c.Hash, Line: c.Line, Ambiguous: tied}
		if t == Context {
			d.Context = append(d.Context, st)
		} else {
			d.Statements = append(d.Statements, st)
		}
	}
	var out []Draft
	for _, k := range order {
		d := by[k]
		if len(d.Statements) == 0 {
			continue // pure framing prose proposes nothing
		}
		out = append(out, rederive(*d))
	}
	return out
}

// rederive recomputes everything a draft derives from its statements' text,
// types and section: clause hashes, DerivedFrom, Anchor, and the receipt.
// Map calls it on every draft, so no stored field is ever trusted.
func rederive(d Draft) Draft {
	st := make([]Statement, len(d.Statements))
	copy(st, d.Statements)
	d.Statements = st
	d.DerivedFrom = nil
	for i := range d.Statements {
		d.Statements[i].ClauseHash = ClauseHash(d.Statements[i].Text)
		d.DerivedFrom = append(d.DerivedFrom, d.Statements[i].ClauseHash)
	}
	slices.Sort(d.DerivedFrom)
	d.Anchor = anchor(d.Section)
	d.Receipt = receipt(d)
	return d
}

func sectionKey(path []string) string {
	var parts []string
	for _, p := range path {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "(top)"
	}
	return strings.Join(parts, " › ")
}

func anchor(section string) string {
	sum := sha256.Sum256([]byte(Normalize(section)))
	return hex.EncodeToString(sum[:])
}

// Render writes a draft back as markdown, the form a human reviews.
func Render(d Draft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", d.Section)
	for _, s := range d.Statements {
		fmt.Fprintf(&b, "- %s\n", s.Text)
	}
	return b.String()
}

// receipt re-extracts the rendered draft with the same frozen classifier.
func receipt(d Draft) Receipt {
	r := Receipt{}
	type got struct {
		t    Type
		tied bool
	}
	again := map[string][]got{}
	n := 0
	for _, c := range Segment(Render(d)) {
		t, _, tied := classify(c.Text, c.FromList)
		again[c.Hash] = append(again[c.Hash], got{t, tied})
		n++
	}
	if n != len(d.Statements) {
		r.Reason = fmt.Sprintf("re-extraction yields %d statements, drafted %d: the rendering is not lossless", n, len(d.Statements))
		return r
	}
	var ambiguous []string
	for _, s := range d.Statements {
		g := again[s.ClauseHash]
		if len(g) == 0 {
			r.Reason = fmt.Sprintf("line %d does not survive re-extraction", s.Line)
			return r
		}
		again[s.ClauseHash] = g[1:]
		if g[0].t != s.Type {
			r.Reason = fmt.Sprintf("line %d re-extracts as %s, drafted as %s", s.Line, g[0].t, s.Type)
			return r
		}
		if s.Ambiguous || g[0].tied {
			ambiguous = append(ambiguous, fmt.Sprintf("line %d", s.Line))
		}
	}
	if len(ambiguous) > 0 {
		r.Reason = fmt.Sprintf("ambiguous type (two types tie) at %s: reword so one reading wins", strings.Join(ambiguous, ", "))
		return r
	}
	r.Validated = true
	r.Reason = fmt.Sprintf("%d statements re-extract identically, none ambiguous", len(d.Statements))
	return r
}
