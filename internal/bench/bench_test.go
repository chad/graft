package bench

import (
	"bytes"
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
	r := Record{Case: "todo", Arm: arm, Commit: commit, Fixture: "fx1", Resolution: res}
	for _, o := range outcomes {
		s := Sample{Outcome: o, Checks: 10, Passed: 10}
		if o != Works {
			s.Passed = 3
			s.Layer = LayerConvergence
		}
		if o == Unreachable {
			s.Checks, s.Passed, s.Layer = 0, 0, ""
		}
		r.Samples = append(r.Samples, s)
	}
	return r
}

func five(o Outcome) []Outcome { return []Outcome{o, o, o, o, o} }

func TestCitable(t *testing.T) {
	cases := map[string]struct {
		r    Record
		want bool
	}{
		"coarse full":   {rec(ArmGraft, "c1", Coarse, five(Works)...), true},
		"smoke":         {rec(ArmGraft, "c1", Smoke, Works, Works), false},
		"unstated":      {rec(ArmGraft, "c1", Unstated, five(Works)...), false},
		"short":         {rec(ArmGraft, "c1", Coarse, Works, Works), false},
		"dirty":         {func() Record { r := rec(ArmGraft, "c1", Coarse, five(Works)...); r.Dirty = true; return r }(), false},
		"no resolution": {rec(ArmGraft, "c1", "", five(Works)...), false},
	}
	for name, c := range cases {
		if ok, why := c.r.Citable(); ok != c.want {
			t.Errorf("%s: citable=%v (%s)", name, ok, why)
		}
	}
}

func TestStoreAppendLoadAndRefusals(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := s.Append(rec(ArmGraft, "c1", Coarse, five(Works)...)); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(rec(ArmBaseline, "c1", Coarse, five(Works)...)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("todo")
	if err != nil || len(got) != 2 || got[0].Schema != Schema || got[0].At.IsZero() {
		t.Fatalf("%v %+v", err, got)
	}
	bad := []Record{
		rec("oracle", "c1", Coarse, Works),
		rec(ArmGraft, "c1", Coarse, "great"),
		func() Record { r := rec(ArmGraft, "c1", Coarse, Works); r.Case = "../escape"; return r }(),
		func() Record { r := rec(ArmGraft, "c1", Coarse, Works); r.Samples[0].Passed = 11; return r }(),
	}
	for i, r := range bad {
		if err := s.Append(r); err == nil {
			t.Errorf("bad record %d accepted", i)
		}
	}
}

func TestAggregateNeverPoolsAcrossCommitsOrFixtures(t *testing.T) {
	recs := []Record{
		rec(ArmGraft, "before-fix", Coarse, five(Broke)...),
		rec(ArmGraft, "after-fix", Coarse, five(Works)...),
		func() Record { r := rec(ArmGraft, "after-fix", Coarse, five(Works)...); r.Fixture = "fx2"; return r }(),
	}
	parts, _, err := Aggregate(recs)
	if err != nil || len(parts) != 3 {
		t.Fatalf("%v: %d partitions, want 3", err, len(parts))
	}
	for _, p := range parts {
		if p.Works.N != 5 {
			t.Fatalf("pooled: %+v", p)
		}
	}
}

func TestUnreachableIsNotInTheDenominator(t *testing.T) {
	parts, _, err := Aggregate([]Record{rec(ArmGraft, "c1", Coarse, Works, Works, Works, Works, Unreachable)})
	if err != nil {
		t.Fatal(err)
	}
	if p := parts[0]; p.Works.K != 4 || p.Works.N != 4 || p.Outcomes[Unreachable] != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestFailuresAreCountedByLayer(t *testing.T) {
	r := rec(ArmGraft, "c1", Coarse, Works, Works, Disagreed, Broke, Broke)
	r.Samples[3].Layer = LayerSelection
	r.Samples[4].Layer = ""
	parts, _, _ := Aggregate([]Record{r})
	got := parts[0].ByLayer
	if got[LayerConvergence] != 1 || got[LayerSelection] != 1 || got[LayerNone] != 1 {
		t.Fatalf("%v", got)
	}
}

func TestReportShowsExcludedAndOnlyPermittedComparisons(t *testing.T) {
	recs := []Record{
		rec(ArmGraft, "c1", Coarse, Works, Works, Works, Works, Disagreed),
		rec(ArmBaseline, "c1", Coarse, five(Works)...),
		rec(ArmIntent, "c1", Coarse, five(Broke)...),
		rec(ArmGraft, "c1", Smoke, Works, Works),
	}
	var b bytes.Buffer
	if err := Report(&b, "todo", recs); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"4/5 [0.38, 0.96]",
		"do NOT distinguish graft 4/5 [0.38, 0.96] from baseline 5/5 [0.57, 1.00]",
		"do NOT distinguish graft 4/5 [0.38, 0.96] from intent 0/5 [0.00, 0.43]", // 0.38 <= 0.43: n=5 cannot tell these apart
		"Shown, counted in nothing (1)",
		"smoke run",
		"convergence 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"better", "worse", "%", "p="} {
		if strings.Contains(out, banned) {
			t.Errorf("report contains %q:\n%s", banned, out)
		}
	}
}
