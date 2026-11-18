package bench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustW(t *testing.T, k, n int) Interval {
	t.Helper()
	iv, err := Wilson(k, n)
	if err != nil {
		t.Fatal(err)
	}
	return iv
}

// Reference values: Phoenix's FINDINGS (0/5 [0.00, 0.43], 5/5 [0.57, 1.00]) and
// its stats doc (34/50 [0.54, 0.79]).
func TestWilsonKnownValues(t *testing.T) {
	for _, c := range []struct {
		k, n int
		want string
	}{
		{0, 5, "0/5 [0.00, 0.43]"},
		{5, 5, "5/5 [0.57, 1.00]"},
		{34, 50, "34/50 [0.54, 0.79]"},
		{2, 5, "2/5 [0.12, 0.77]"},
		{120, 120, "120/120 [0.97, 1.00]"},
	} {
		if got := mustW(t, c.k, c.n).String(); got != c.want {
			t.Errorf("Wilson(%d,%d) = %s, want %s", c.k, c.n, got, c.want)
		}
	}
}

func TestWilsonEmptyIsAbsentNotZero(t *testing.T) {
	iv := mustW(t, 0, 0)
	if iv.HasRate || iv.Lo != 0 || iv.Hi != 1 || iv.String() != "—/0 (no samples)" {
		t.Fatalf("%+v", iv)
	}
	if _, err := Wilson(3, 2); err == nil {
		t.Fatal("k > n accepted")
	}
}

func TestCompareNeverGivesAVerdictOnOverlap(t *testing.T) {
	a, b := mustW(t, 4, 5), mustW(t, 5, 5)
	if s := Compare("graft", a, "baseline", b); !strings.Contains(s, "do NOT distinguish") {
		t.Fatal(s)
	}
	c, d := mustW(t, 0, 5), mustW(t, 5, 5)
	if s := Compare("graft", c, "baseline", d); !strings.Contains(s, "distinguish") || strings.Contains(s, "better") {
		t.Fatal(s)
	}
	if s := Compare("graft", mustW(t, 0, 0), "baseline", d); !strings.Contains(s, "cannot be compared") {
		t.Fatal(s)
	}
}

func rec(arm Arm, commit string, res Resolution, outcomes ...Outcome) Record {
	r := Record{Case: "todo", Arm: arm, Commit: commit, Fixture: "fx1", Model: "m1", Resolution: res}
	for _, o := range outcomes {
		s := Sample{Outcome: o, Checks: 10, Passed: 10}
		if o != Works {
			s.Passed = 3
		}
		if o == Unreachable {
			s.Checks, s.Passed = 0, 0
		}
		r.Samples = append(r.Samples, s)
	}
	return r
}

func five(o Outcome) []Outcome { return []Outcome{o, o, o, o, o} }

func with(r Record, f func(*Record)) Record { f(&r); return r }

func TestCitable(t *testing.T) {
	cases := map[string]struct {
		r    Record
		want bool
	}{
		"coarse full":        {rec(ArmGraft, "c1", Coarse, five(Works)...), true},
		"smoke":              {rec(ArmGraft, "c1", Smoke, Works, Works), false},
		"unstated":           {rec(ArmGraft, "c1", Unstated, five(Works)...), false},
		"short":              {rec(ArmGraft, "c1", Coarse, Works, Works), false},
		"dirty":              {with(rec(ArmGraft, "c1", Coarse, five(Works)...), func(r *Record) { r.Dirty = true }), false},
		"no resolution":      {rec(ArmGraft, "c1", "", five(Works)...), false},
		"unknown resolution": {rec(ArmGraft, "c1", "huge", five(Works)...), false},
		"all unreachable":    {rec(ArmGraft, "c1", Coarse, five(Unreachable)...), false},
	}
	for name, c := range cases {
		if ok, why := c.r.Citable(); ok != c.want {
			t.Errorf("%s: citable=%v (%s)", name, ok, why)
		}
	}
}

func TestStoreAppendLoadAndRefusals(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	a, err := s.Append(rec(ArmGraft, "c1", Coarse, five(Works)...))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Append(rec(ArmBaseline, "c1", Coarse, five(Works)...))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}
	got, bad, err := s.Load("todo")
	if err != nil || len(bad) != 0 || len(got) != 2 || got[0].Schema != Schema || got[0].ID != a.ID {
		t.Fatalf("%v %v %+v", err, bad, got)
	}
	for i, r := range []Record{
		rec("oracle", "c1", Coarse, Works),
		rec(ArmGraft, "c1", Coarse, "great"),
		with(rec(ArmGraft, "c1", Coarse, Works), func(r *Record) { r.Case = "../escape" }),
		with(rec(ArmGraft, "c1", Coarse, Works), func(r *Record) { r.Samples[0].Passed = 11 }),
	} {
		if _, err := s.Append(r); err == nil {
			t.Errorf("bad record %d accepted", i)
		}
	}
}

// Review on #8: a torn last line must not glue onto the next append and
// poison the whole case.
func TestTornLineDoesNotPoisonTheCase(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, err := s.Append(rec(ArmGraft, "c1", Coarse, five(Works)...)); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(s.Dir, "todo.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"schema":2,"case":"todo","arm":"gra`)
	f.Close()
	if _, err := s.Append(rec(ArmBaseline, "c1", Coarse, five(Works)...)); err != nil {
		t.Fatal(err)
	}
	got, bad, err := s.Load("todo")
	if err != nil || len(got) != 2 || len(bad) != 1 || bad[0].Line != 2 {
		t.Fatalf("records=%d corrupt=%+v err=%v", len(got), bad, err)
	}
	var b bytes.Buffer
	Report(&b, "todo", got, nil, bad)
	if !strings.Contains(b.String(), "Corrupt log lines, counted in nothing (1)") {
		t.Fatal(b.String())
	}
}

func TestAttribution(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	r, _ := s.Append(rec(ArmGraft, "c1", Coarse, Works, Works, Disagreed, Broke, Broke))
	ok := []Attribution{
		{RecordID: r.ID, SampleIdx: 2, Layers: []Layer{LayerConvergence}, Reason: "gate passed, behaviour wrong", By: "x"},
		{RecordID: r.ID, SampleIdx: 3, Layers: []Layer{LayerSelection, LayerRender}, Reason: "both", By: "x"},
		{RecordID: r.ID, SampleIdx: 3, Layers: []Layer{LayerSelection}, Reason: "re-attributed", By: "x"}, // latest wins
	}
	for _, a := range ok {
		if err := s.Attribute("todo", a); err != nil {
			t.Fatal(err)
		}
	}
	for i, a := range []Attribution{
		{RecordID: r.ID, SampleIdx: 0, Layers: []Layer{LayerRender}},               // a working sample
		{RecordID: r.ID, SampleIdx: 9, Layers: []Layer{LayerRender}},               // no such sample
		{RecordID: "nope", SampleIdx: 2, Layers: []Layer{LayerRender}},             // no such record
		{RecordID: r.ID, SampleIdx: 2, Layers: []Layer{"vibes"}},                   // unknown layer
		{RecordID: r.ID, SampleIdx: 2},                                             // no layer
		{RecordID: r.ID, SampleIdx: 2, Layers: []Layer{LayerUnknown, LayerRender}}, // unknown + named
	} {
		if err := s.Attribute("todo", a); err == nil {
			t.Errorf("bad attribution %d accepted", i)
		}
	}
	recs, _, _ := s.Load("todo")
	attrs, _, _ := s.LoadAttributions("todo")
	parts, _, err := Aggregate(recs, attrs)
	if err != nil {
		t.Fatal(err)
	}
	p := parts[0]
	if p.ByLayer[LayerConvergence] != 1 || p.ByLayer[LayerSelection] != 1 || p.ByLayer[LayerRender] != 0 || p.Unattributed != 1 {
		t.Fatalf("%+v unattributed=%d", p.ByLayer, p.Unattributed)
	}
}

func TestAggregateNeverPoolsAcrossCommitsFixturesOrModels(t *testing.T) {
	recs := []Record{
		rec(ArmGraft, "before-fix", Coarse, five(Broke)...),
		rec(ArmGraft, "after-fix", Coarse, five(Works)...),
		with(rec(ArmGraft, "after-fix", Coarse, five(Works)...), func(r *Record) { r.Fixture = "fx2" }),
		with(rec(ArmGraft, "after-fix", Coarse, five(Works)...), func(r *Record) { r.Model = "m2" }),
	}
	parts, _, err := Aggregate(recs, nil)
	if err != nil || len(parts) != 4 {
		t.Fatalf("%v: %d partitions, want 4", err, len(parts))
	}
	for _, p := range parts {
		if p.Works.N != 5 {
			t.Fatalf("pooled: %+v", p)
		}
	}
}

// Review on #8: comparisons only within one fixture, commit and model.
func TestNoComparisonAcrossCommitsOrModels(t *testing.T) {
	recs := []Record{
		rec(ArmGraft, "c1", Coarse, five(Works)...),
		rec(ArmBaseline, "c2", Coarse, five(Broke)...),
		with(rec(ArmIntent, "c1", Coarse, five(Broke)...), func(r *Record) { r.Model = "m2" }),
	}
	var b bytes.Buffer
	if err := Report(&b, "todo", recs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "distinguish") {
		t.Fatalf("compared across commit or model:\n%s", b.String())
	}
}

func TestUnreachableIsNotInTheDenominator(t *testing.T) {
	parts, _, err := Aggregate([]Record{rec(ArmGraft, "c1", Coarse, Works, Works, Works, Works, Unreachable)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := parts[0]; p.Works.K != 4 || p.Works.N != 4 || p.Outcomes[Unreachable] != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestReportShowsExcludedAndOnlyPermittedComparisons(t *testing.T) {
	recs := []Record{
		rec(ArmGraft, "c1", Coarse, Works, Works, Works, Works, Disagreed),
		rec(ArmBaseline, "c1", Coarse, five(Works)...),
		rec(ArmIntent, "c1", Coarse, five(Broke)...),
		rec(ArmGraft, "c1", Smoke, Works, Works),
		// a second cell with a disjoint comparison, so the "distinguish" sentence is checked too
		with(rec(ArmGraft, "c2", Fine, make([]Outcome, 30)...), func(r *Record) {
			for i := range r.Samples {
				r.Samples[i] = Sample{Outcome: Works, Checks: 1, Passed: 1}
			}
		}),
		with(rec(ArmIntent, "c2", Fine, make([]Outcome, 30)...), func(r *Record) {
			for i := range r.Samples {
				r.Samples[i] = Sample{Outcome: Broke}
			}
		}),
	}
	var b bytes.Buffer
	if err := Report(&b, "todo", recs, nil, nil); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"4/5 [0.38, 0.96]",
		"do NOT distinguish graft 4/5 [0.38, 0.96] from baseline 5/5 [0.57, 1.00]",
		"do NOT distinguish graft 4/5 [0.38, 0.96] from intent 0/5 [0.00, 0.43]", // 0.38 <= 0.43
		"These runs distinguish graft 30/30 [0.89, 1.00] from intent 0/30 [0.00, 0.11] (intervals disjoint)",
		"Shown, counted in nothing (1)",
		"smoke run",
		"unattributed 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"better", "worse", "%", "p=", "significant"} {
		if strings.Contains(out, banned) {
			t.Errorf("report contains %q:\n%s", banned, out)
		}
	}
}
