package drafter

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		text     string
		fromList bool
		want     Type
	}{
		{"The system must send a confirmation email", true, Requirement},
		{"Passwords must not be stored in plain text", true, Constraint},
		{"A loan is always owned by exactly one member", true, Invariant},
		{"A member means a person registered with the library", true, Definition},
		// The bench regression: declarative interface lines used to be CONTEXT.
		{"`POST /tasks` — creates a task from a JSON body; returns 201", true, Requirement},
		{"Users can list every task", true, Requirement},
		{"A book has a title, an author and a number of copies", true, Definition},
		{"The priority is one of low, normal, high, and defaults to normal", true, Definition},
		{"A loan is due 14 days after it is borrowed", true, Constraint},
		{"404 — the task does not exist", true, Definition},
		// Shell routes are not promoted.
		{"`GET /health`", true, Context},
		// Framing prose stays context.
		{"The library lends books to its members across town", false, Context},
	}
	for _, c := range cases {
		if got, _ := Classify(c.text, c.fromList); got != c.want {
			t.Errorf("Classify(%q) = %s, want %s", c.text, got, c.want)
		}
	}
}

func TestCapabilityExcludesCanNot(t *testing.T) {
	if capability("users can not delete loans") {
		t.Fatal("`can not` counted as a capability")
	}
	if !capability("users can delete loans") {
		t.Fatal("capability missed")
	}
}

const spec = `# Library

This service lends books.

## Books

- A book has a title, an author and a number of copies
- ` + "`GET /books`" + ` — lists every book; returns 200
- Users can borrow a book when a copy is available

## Loans

- A loan is due 14 days after it is borrowed
- A member cannot hold more than 3 loans at once

` + "```json\n{\"ignored\": true}\n```\n"

func TestSegmentKeepsSectionsLinesAndSkipsFences(t *testing.T) {
	cs := Segment(spec)
	if len(cs) != 6 {
		for _, c := range cs {
			t.Log(c.Section, c.Line, c.Text)
		}
		t.Fatalf("got %d clauses, want 6", len(cs))
	}
	if got := sectionKey(cs[1].Section); got != "Library › Books" {
		t.Fatalf("section %q", got)
	}
	if cs[1].Line != 7 || !cs[1].FromList {
		t.Fatalf("clause %+v", cs[1])
	}
	for _, c := range cs {
		if strings.Contains(c.Text, "ignored") {
			t.Fatal("fenced code became a clause")
		}
	}
}

func TestWrappedListItemIsOneClause(t *testing.T) {
	cs := Segment("- Two members must never share an email; a repeat\n  is a conflict, not a validation error\n- Next\n")
	if len(cs) != 2 || !strings.HasSuffix(cs[0].Text, "not a validation error") || cs[0].Line != 1 {
		t.Fatalf("got %+v", cs)
	}
}

func TestClauseHashIgnoresCosmetics(t *testing.T) {
	if ClauseHash("A  book has a title.") != ClauseHash("a book has a title") {
		t.Fatal("cosmetic edit changed the hash")
	}
	if ClauseHash("a book has a title") == ClauseHash("a book has an author") {
		t.Fatal("meaning change kept the hash")
	}
}

func TestDraftsCarryValidReceiptsAndKeepContext(t *testing.T) {
	ds := Drafts(spec)
	if len(ds) != 2 {
		t.Fatalf("got %d drafts, want 2 (Books, Loans); the framing paragraph proposes nothing", len(ds))
	}
	for _, d := range ds {
		if !d.Receipt.Validated {
			t.Errorf("%s: %s", d.Section, d.Receipt.Reason)
		}
		if d.Tier != "blueprint" {
			t.Errorf("%s: tier %q", d.Section, d.Tier)
		}
	}
	if len(ds[0].Statements) != 3 {
		t.Fatalf("Books has %d statements, want 3", len(ds[0].Statements))
	}
}

func acceptAll(ds []Draft) []Accepted {
	var a []Accepted
	for i, d := range ds {
		a = append(a, Accepted{ID: []string{"prov-books", "prov-loans"}[i], DerivedFrom: d.DerivedFrom, Anchor: d.Anchor})
	}
	return a
}

func TestRerunOnUnchangedSpecRevisesSameIDs(t *testing.T) {
	acc := acceptAll(Drafts(spec))
	for _, p := range Map(Drafts(spec), acc) {
		if p.Kind != Revise {
			t.Fatalf("%s: %s (%s)", p.Draft.Section, p.Kind, p.Reason)
		}
	}
}

func TestRewordingOneClauseStillRevises(t *testing.T) {
	acc := acceptAll(Drafts(spec))
	edited := strings.Replace(spec, "Users can borrow a book when a copy is available",
		"Users can borrow a book while at least one copy is on the shelf", 1)
	ps := Map(Drafts(edited), acc)
	if ps[0].Kind != Revise || ps[0].RecordID != "prov-books" {
		t.Fatalf("got %s %s (%s)", ps[0].Kind, ps[0].RecordID, ps[0].Reason)
	}
}

func TestNewSectionIsNewWithNoID(t *testing.T) {
	acc := acceptAll(Drafts(spec))
	more := spec + "\n## Members\n\n- A member has a name and an email address\n"
	ps := Map(Drafts(more), acc)
	last := ps[len(ps)-1]
	if last.Kind != New || last.RecordID != "" {
		t.Fatalf("got %s id=%q", last.Kind, last.RecordID)
	}
}

func TestRewrittenSectionIsAQuestionNotANewID(t *testing.T) {
	acc := acceptAll(Drafts(spec))
	rewritten := `# Library

## Loans

- Every loan lasts two weeks from the day it starts
- Each member may hold no more than 3 loans
`
	ps := Map(Drafts(rewritten), acc)
	if len(ps) != 1 || ps[0].Kind != Question || ps[0].Candidates[0] != "prov-loans" {
		t.Fatalf("got %+v", ps)
	}
}

func TestSplitIsAQuestion(t *testing.T) {
	acc := acceptAll(Drafts(spec))
	split := `# Library

## Book records

- A book has a title, an author and a number of copies
- ` + "`GET /books`" + ` — lists every book; returns 200

## Borrowing

- Users can borrow a book when a copy is available
- ` + "`GET /books`" + ` — lists every book; returns 200
`
	for _, p := range Map(Drafts(split), acc) {
		if p.Kind != Question {
			t.Fatalf("%s: %s, want question (split)", p.Draft.Section, p.Kind)
		}
	}
}
