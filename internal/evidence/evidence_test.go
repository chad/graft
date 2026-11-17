package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var base = Pin{RecordID: "prov-1", RecordVersion: 3, GenHash: "g3", RegionHash: "r1"}

func newLog(t *testing.T) (*Log, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "evidence.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { tick = tick.Add(time.Second); return tick }
	return l, p
}

func mustAppend(t *testing.T, l *Log, e Evidence) Evidence {
	t.Helper()
	got, err := l.Append(e)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFreshWhenPinMatches(t *testing.T) {
	e := Evidence{Pin: base}
	if e.Stale(base) {
		t.Fatalf("stale against its own pin: %v", e.Moved(base))
	}
}

func TestStaleWhenAnyOfThreeMoves(t *testing.T) {
	cases := map[string]func(p *Pin){
		"record_version": func(p *Pin) { p.RecordVersion++ },
		"gen_hash":       func(p *Pin) { p.GenHash = "g4" },
		"region_hash":    func(p *Pin) { p.RegionHash = "r2" },
	}
	for want, mutate := range cases {
		t.Run(want, func(t *testing.T) {
			cur := base
			mutate(&cur)
			e := Evidence{Pin: base}
			if got := e.Moved(cur); !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("Moved = %v, want [%s]", got, want)
			}
		})
	}
}

func TestDifferentRecordIsNotEvidenceForIt(t *testing.T) {
	cur := base
	cur.RecordID = "prov-2"
	if got := (Evidence{Pin: base}).Moved(cur); !reflect.DeepEqual(got, []string{"record_id"}) {
		t.Fatalf("Moved = %v", got)
	}
}

func TestAppendChainsAndReloads(t *testing.T) {
	l, p := newLog(t)
	a := mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base, By: "ci"})
	b := mustAppend(t, l, Evidence{Kind: Eval, Verdict: Pass, Pin: base, By: "graft"})
	if a.Prev != "" || b.Prev != a.ID || a.ID == "" || b.ID == a.ID {
		t.Fatalf("bad chain: a=%+v b=%+v", a, b)
	}
	l2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Verify(); err != nil {
		t.Fatalf("reloaded log does not verify: %v", err)
	}
	if got := l2.All(); len(got) != 2 || got[1].ID != b.ID {
		t.Fatalf("reloaded %d entries", len(got))
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	l, p := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	raw, _ := os.ReadFile(p)
	// Rewrite history: the first run "passed".
	tampered := strings.Replace(string(raw), `"verdict":"fail"`, `"verdict":"pass"`, 1)
	if err := os.WriteFile(p, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Verify(); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("Verify = %v, want ErrBrokenChain", err)
	}
}

func TestAppendRequiresKindVerdictRecord(t *testing.T) {
	l, _ := newLog(t)
	if _, err := l.Append(Evidence{Verdict: Pass, Pin: base}); err == nil {
		t.Fatal("accepted evidence without a kind")
	}
}

func TestFreshFiltersStale(t *testing.T) {
	l, _ := newLog(t)
	old := base
	old.RegionHash = "r0"
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: old})
	keep := mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if got := l.Fresh(base); len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("Fresh = %+v", got)
	}
}

func TestPolicyLatestFreshVerdictDecides(t *testing.T) {
	pol := Policy{"high": {TestRun, HumanSignoff}, "low": {TestRun}}
	l, _ := newLog(t)

	if r := pol.Evaluate(l, "high", base); r.Satisfied || len(r.Missing) != 2 {
		t.Fatalf("empty log: %+v", r)
	}
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	if r := pol.Evaluate(l, "low", base); r.Satisfied || !reflect.DeepEqual(r.Failing, []Kind{TestRun}) {
		t.Fatalf("after fail: %+v", r)
	}
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if r := pol.Evaluate(l, "low", base); !r.Satisfied {
		t.Fatalf("latest pass should satisfy low: %+v", r)
	}
	if r := pol.Evaluate(l, "high", base); r.Satisfied || !reflect.DeepEqual(r.Missing, []Kind{HumanSignoff}) {
		t.Fatalf("high still needs signoff: %+v", r)
	}
	if r := pol.Evaluate(l, "unlisted", base); !r.Satisfied {
		t.Fatalf("unlisted tier requires nothing: %+v", r)
	}
}

func TestStaleEvidenceNeverSatisfiesPolicy(t *testing.T) {
	pol := Policy{"low": {TestRun}}
	l, _ := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	moved := base
	moved.GenHash = "g4" // the record changed what drives generation
	if r := pol.Evaluate(l, "low", moved); r.Satisfied {
		t.Fatalf("stale pass satisfied policy: %+v", r)
	}
}
