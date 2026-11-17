package evidence

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var base = Pin{RecordID: "prov-1", Version: 3, GenHash: "g3", RegionHash: "r1", SpecHash: "s1"}

func newLog(t *testing.T) (*Log, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "evidence.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	l.now = func() time.Time { mu.Lock(); defer mu.Unlock(); tick = tick.Add(time.Second); return tick }
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
	if e := (Evidence{Pin: base}); e.Stale(base) {
		t.Fatalf("stale against its own pin: %v", e.Moved(base))
	}
}

func TestStaleWhenWhatItReadMoves(t *testing.T) {
	cases := map[string]func(p *Pin){
		"gen_hash":    func(p *Pin) { p.GenHash = "g4" },
		"region_hash": func(p *Pin) { p.RegionHash = "r2" },
		"spec_hash":   func(p *Pin) { p.SpecHash = "s2" },
	}
	for want, mutate := range cases {
		t.Run(want, func(t *testing.T) {
			cur := base
			mutate(&cur)
			if got := (Evidence{Pin: base}).Moved(cur); !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("Moved = %v, want [%s]", got, want)
			}
		})
	}
}

// PR #1 review finding 4: a version bump that changes nothing evidence read
// (a status flip) does not make evidence stale; that matches records.Stale.
func TestVersionBumpAloneIsNotStale(t *testing.T) {
	cur := base
	cur.Version++
	if (Evidence{Pin: base}).Stale(cur) {
		t.Fatal("version-only bump made evidence stale")
	}
}

func TestDifferentRecordIsNotEvidenceForIt(t *testing.T) {
	cur := base
	cur.RecordID = "prov-2"
	if got := (Evidence{Pin: base}).Moved(cur); !reflect.DeepEqual(got, []string{"record_id"}) {
		t.Fatalf("Moved = %v", got)
	}
}

// Pins the encoding: if this changes, every existing ID changes, so bump FormatVersion.
func TestIDTestVector(t *testing.T) {
	e := Evidence{V: 1, Kind: TestRun, Verdict: Pass, Pin: base, By: "ci",
		At: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	id, err := computeID(e)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ca4d1d250c3c265b440872cd2f5374c3b53f274ca3ea449f48940495adab520e"
	if id != want {
		t.Fatalf("ID = %s; if the encoding change is intended, bump FormatVersion and update this vector", id)
	}
}

func TestAppendChainsAndReloads(t *testing.T) {
	l, p := newLog(t)
	a := mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base, By: "ci"})
	b := mustAppend(t, l, Evidence{Kind: Eval, Verdict: Pass, Pin: base, By: "graft"})
	if a.Prev != "" || b.Prev != a.ID || a.ID == "" || b.ID == a.ID || a.V != FormatVersion {
		t.Fatalf("bad chain: a=%+v b=%+v", a, b)
	}
	l2, err := Open(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := l2.All(); len(got) != 2 || got[1].ID != b.ID {
		t.Fatalf("reloaded %d entries", len(got))
	}
}

func rewrite(t *testing.T, p string, f func([]string) []string) {
	t.Helper()
	raw, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	out := strings.Join(f(lines), "\n")
	if out != "" {
		out += "\n"
	}
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	cases := map[string]func([]string) []string{
		"edited": func(ls []string) []string {
			ls[0] = strings.Replace(ls[0], `"verdict":"fail"`, `"verdict":"pass"`, 1)
			return ls
		},
		"tail deleted": func(ls []string) []string { return ls[:len(ls)-1] },
		"reordered":    func(ls []string) []string { ls[0], ls[1] = ls[1], ls[0]; return ls },
		"emptied":      func([]string) []string { return nil },
		"middle cut":   func(ls []string) []string { return append(ls[:1], ls[2:]...) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			l, p := newLog(t)
			mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
			mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
			mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
			rewrite(t, p, mutate)
			if _, err := Open(p); !errors.Is(err, ErrBrokenChain) {
				t.Fatalf("Open = %v, want ErrBrokenChain", err)
			}
			if _, err := OpenUnverified(p); err != nil {
				t.Fatalf("OpenUnverified should load for inspection: %v", err)
			}
		})
	}
}

// A crash mid-write leaves a partial last line: readers ignore it, the next
// writer truncates it. It never bricks the log.
func TestPartialLastLineIsRecovered(t *testing.T) {
	l, p := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"v":1,"id":"trunc`)
	f.Close()
	l2, err := Open(p)
	if err != nil {
		t.Fatalf("Open with a partial tail: %v", err)
	}
	if n := len(l2.All()); n != 1 {
		t.Fatalf("%d entries", n)
	}
	mustAppend(t, l2, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if l3, err := Open(p); err != nil || len(l3.All()) != 2 {
		t.Fatalf("after repair: %v", err)
	}
}

// A crash after the entry's fsync but before the head moved leaves the head
// one behind. The head is a lower bound: Open accepts it, Append rolls it forward.
func TestCrashBeforeHeadAdvanceIsRecovered(t *testing.T) {
	l, p := newLog(t)
	a := mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	if err := writeHead(p, head{V: FormatVersion, Count: 1, Last: a.ID}); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(p)
	if err != nil {
		t.Fatalf("Open with a lagging head: %v", err)
	}
	if n := len(l2.All()); n != 2 {
		t.Fatalf("uncommitted-but-chained entry dropped: %d", n)
	}
	mustAppend(t, l2, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if h, _, _ := readHead(p); h.Count != 3 {
		t.Fatalf("head not rolled forward: %+v", h)
	}
}

func TestAppendRefusesBrokenLog(t *testing.T) {
	l, p := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	rewrite(t, p, func(ls []string) []string { return ls[:1] })
	if _, err := l.Append(Evidence{Kind: TestRun, Verdict: Pass, Pin: base}); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("Append on a truncated log = %v", err)
	}
}

func TestAppendRejectsTimeGoingBackwards(t *testing.T) {
	l, _ := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base, At: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)})
	_, err := l.Append(Evidence{Kind: TestRun, Verdict: Fail, Pin: base, At: time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)})
	if !errors.Is(err, ErrTimeBackwards) {
		t.Fatalf("Append = %v", err)
	}
}

func TestAppendRequiresKindVerdictRecord(t *testing.T) {
	l, _ := newLog(t)
	if _, err := l.Append(Evidence{Verdict: Pass, Pin: base}); err == nil {
		t.Fatal("accepted evidence without a kind")
	}
}

// Two Log values on one file (as two processes would have) must not fork the chain.
func TestTwoWritersOnOneFileKeepOneChain(t *testing.T) {
	_, p := newLog(t)
	a, _ := Open(p)
	b, _ := Open(p)
	var wg sync.WaitGroup
	for _, l := range []*Log{a, b} {
		wg.Add(1)
		go func(l *Log) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if _, err := l.Append(Evidence{Kind: TestRun, Verdict: Pass, Pin: base}); err != nil {
					t.Error(err)
					return
				}
			}
		}(l)
	}
	wg.Wait()
	l, err := Open(p)
	if err != nil {
		t.Fatalf("chain forked: %v", err)
	}
	if n := len(l.All()); n != 50 {
		t.Fatalf("%d entries, want 50", n)
	}
}

// A crash after the very first entry's fsync, before any head was written.
func TestCrashBeforeFirstHeadIsRecovered(t *testing.T) {
	l, p := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if err := os.Remove(p + ".head"); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(p)
	if err != nil {
		t.Fatalf("Open with one entry and no head: %v", err)
	}
	mustAppend(t, l2, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if h, ok, _ := readHead(p); !ok || h.Count != 2 {
		t.Fatalf("head not rolled forward: %+v", h)
	}
}

func TestHeadWithoutLogIsBroken(t *testing.T) {
	l, p := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	os.Remove(p)
	if _, err := Open(p); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("Open = %v", err)
	}
}

// Readers take a shared lock and read log and head together: no false
// ErrBrokenChain while a writer is mid-append.
func TestReadersRacingAWriterNeverSeeABrokenChain(t *testing.T) {
	w, p := newLog(t)
	mustAppend(t, w, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if _, err := w.Append(Evidence{Kind: TestRun, Verdict: Pass, Pin: base}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		if _, err := Open(p); err != nil {
			t.Fatalf("Open %d raced a writer: %v", i, err)
		}
	}
	<-done
}

// A handle opened before another writer appended evaluates the current log.
func TestEvaluateSeesOtherWritersAppends(t *testing.T) {
	a, p := newLog(t)
	mustAppend(t, a, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	b, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, a, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	r, err := (Policy{"low": {TestRun}}).Evaluate(b, "low", base)
	if err != nil {
		t.Fatalf("stale handle: %v", err)
	}
	if r.Satisfied {
		t.Fatal("evaluated a stale copy: the later fail was not seen")
	}
}

// The same, across real processes: re-exec the test binary as writers.
func TestTwoProcessesKeepOneChain(t *testing.T) {
	if p := os.Getenv("EVIDENCE_WRITER"); p != "" {
		l, err := OpenUnverified(p)
		if err != nil {
			os.Exit(3)
		}
		for i := 0; i < 20; i++ {
			if _, err := l.Append(Evidence{Kind: TestRun, Verdict: Pass, Pin: base}); err != nil {
				os.Exit(4)
			}
		}
		os.Exit(0)
	}
	p := filepath.Join(t.TempDir(), "evidence.jsonl")
	var cmds []*exec.Cmd
	for i := 0; i < 3; i++ {
		c := exec.Command(os.Args[0], "-test.run=^TestTwoProcessesKeepOneChain$")
		c.Env = append(os.Environ(), "EVIDENCE_WRITER="+p)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("writer: %v", err)
		}
	}
	l, err := Open(p)
	if err != nil {
		t.Fatalf("chain forked across processes: %v", err)
	}
	if n := len(l.All()); n != 60 {
		t.Fatalf("%d entries, want 60", n)
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

func eval(t *testing.T, pol Policy, l *Log, tier string, cur Pin) Result {
	t.Helper()
	r, err := pol.Evaluate(l, tier, cur)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPolicyLatestFreshVerdictDecides(t *testing.T) {
	pol := Policy{"high": {TestRun, HumanSignoff}, "low": {TestRun}, "none": {}}
	l, _ := newLog(t)
	if r := eval(t, pol, l, "high", base); r.Satisfied || len(r.Missing) != 2 {
		t.Fatalf("empty log: %+v", r)
	}
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	if r := eval(t, pol, l, "low", base); r.Satisfied || !reflect.DeepEqual(r.Failing, []Kind{TestRun}) {
		t.Fatalf("after fail: %+v", r)
	}
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	if r := eval(t, pol, l, "low", base); !r.Satisfied {
		t.Fatalf("latest pass should satisfy low: %+v", r)
	}
	if r := eval(t, pol, l, "high", base); r.Satisfied || !reflect.DeepEqual(r.Missing, []Kind{HumanSignoff}) {
		t.Fatalf("high still needs signoff: %+v", r)
	}
	if r := eval(t, pol, l, "none", base); !r.Satisfied {
		t.Fatalf("an explicitly empty tier requires nothing: %+v", r)
	}
}

func TestUnknownTierIsAnError(t *testing.T) {
	l, _ := newLog(t)
	if _, err := (Policy{"low": {TestRun}}).Evaluate(l, "lwo", base); !errors.Is(err, ErrUnknownTier) {
		t.Fatalf("Evaluate = %v", err)
	}
}

func TestEvaluateRefusesTamperedLog(t *testing.T) {
	l, p := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Fail, Pin: base})
	rewrite(t, p, func(ls []string) []string { return ls[:1] }) // cut the fail
	l2, _ := OpenUnverified(p)
	if _, err := (Policy{"low": {TestRun}}).Evaluate(l2, "low", base); !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("Evaluate on a truncated log = %v", err)
	}
}

func TestStaleEvidenceNeverSatisfiesPolicy(t *testing.T) {
	l, _ := newLog(t)
	mustAppend(t, l, Evidence{Kind: TestRun, Verdict: Pass, Pin: base})
	moved := base
	moved.GenHash = "g4"
	if r := eval(t, Policy{"low": {TestRun}}, l, "low", moved); r.Satisfied {
		t.Fatalf("stale pass satisfied policy: %+v", r)
	}
}
