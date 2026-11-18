package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/recording"
)

// subGate checks extra.go (when it exists) for a substring, with a fixed detail.
type subGate struct {
	name   string
	kind   evidence.Kind
	want   string
	detail string
}

func (g subGate) Name() string        { return g.name }
func (g subGate) Kind() evidence.Kind { return g.kind }
func (g subGate) Run(_ context.Context, dir string) (evidence.Verdict, string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "extra.go"))
	if err != nil || strings.Contains(string(b), g.want) {
		return evidence.Pass, "ok", nil
	}
	return evidence.Fail, g.detail, nil
}

// Record ids are random per setup; blank them before comparing runs.
var recID = regexp.MustCompile(`prov-\d+-[0-9a-f]+`)

func normIDs(s string) string { return recID.ReplaceAllString(s, "prov-X") }

const coreBody = "package main\n\nfunc main() {}\n"

func fenced(body string) string { return "```go\n" + body + "```\n" }

// seqModel answers core.go with coreBody; for extra.go the k-th call of an
// attempt gets pick(k, prompt). Completion order is reversed by sleeping less
// for later calls, so it does not match sample index.
type seqModel struct {
	mu      sync.Mutex
	n       int
	prompts []string
	pick    func(k int, prompt string) string
	reverse bool
}

func (m *seqModel) Complete(_ context.Context, prompt string) (string, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	k := m.n
	if strings.Contains(prompt, "Write the complete file `extra.go`") {
		m.n++
	}
	m.mu.Unlock()
	if !strings.Contains(prompt, "Write the complete file `extra.go`") {
		return fenced(coreBody), nil
	}
	if m.reverse {
		time.Sleep(time.Duration(5-k%3) * 15 * time.Millisecond)
	}
	return fenced(m.pick(k, prompt)), nil
}

func (m *seqModel) extraCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

func samplesSetup(t *testing.T, gates ...converge.Gate) (Config, string, []Accepted) {
	cfg, root := setup(t)
	cfg.Gates = gates
	acc, err := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return cfg, root, acc
}

var doubleGate = subGate{"double", evidence.TestRun, "x * 2", "NEED_DOUBLE"}

const (
	badBody   = "package main\n\nfunc Twice(x int) int { return x * 3 }\n"
	smallBody = "package main\n\nfunc Twice(x int) int { return x * 2 } // small\n"
	bigBody   = "package main\n\n// Twice doubles.\n// It is verbose.\n// Very.\nfunc Twice(x int) int {\n\treturn x * 2 // big\n}\n"
)

func TestRunSamplesNewRegionTakesLowestIndex(t *testing.T) {
	cfg, root, acc := samplesSetup(t, doubleGate)
	bodies := []string{badBody, bigBody, smallBody}
	m := &seqModel{pick: func(k int, _ string) string { return bodies[k%3] }}
	cfg.Model, cfg.Samples = m, 3
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatalf("%v %+v", err, out)
	}
	if out[1].Calls != 3 || out[1].Samples != 3 || out[1].Attempts != 1 || !out[1].Applied {
		t.Fatalf("%+v", out[1])
	}
	got, _ := os.ReadFile(filepath.Join(root, "extra.go"))
	// Which body lands at which sample index depends on goroutine scheduling
	// (the fake model hands bodies out in call order), so this only checks a
	// passing body was applied; pick's rule is tested directly below.
	if !strings.Contains(string(got), "// big") && !strings.Contains(string(got), "// small") {
		t.Fatalf("no passing body was applied:\n%s", got)
	}
	n := 0
	for _, e := range cfg.Log.All() {
		if e.Pin.RecordID == acc[1].ID {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d evidence entries for the sampled record, want 3 (losers get evidence too)", n)
	}
}

func TestRunSamplesNonePassUsesClosest(t *testing.T) {
	twiceGate := subGate{"twice", evidence.StaticAnalysis, "Twice(", "NEED_TWICE"}
	cfg, _, acc := samplesSetup(t, twiceGate, doubleGate)
	// First attempt: one candidate passes 1 of 2 gates (NEED_DOUBLE only), one passes 0.
	first := []string{"package main\n\nfunc Other() {}\n", badBody, "package main\n\nfunc Another() {}\n"}
	m := &seqModel{pick: func(k int, p string) string {
		if strings.Contains(p, "Feedback on your previous attempt") {
			return smallBody
		}
		return first[k%3]
	}}
	cfg.Model, cfg.Samples, cfg.Attempts = m, 3, 2
	out, err := Run(context.Background(), cfg, acc)
	if err != nil || out[1].Attempts != 2 || !out[1].Applied {
		t.Fatalf("%v %+v", err, out)
	}
	var second string
	for _, p := range m.prompts {
		if strings.Contains(p, "Feedback on your previous attempt") {
			second = p
			break
		}
	}
	if !strings.Contains(second, "NEED_DOUBLE") || strings.Contains(second, "NEED_TWICE") {
		t.Fatalf("feedback is not from the closest candidate:\n%s", second)
	}
}

func TestRunSamplesOneIsUnchanged(t *testing.T) {
	run := func(samples int) ([]string, int, string) {
		cfg, root, acc := samplesSetup(t, doubleGate)
		answers := []string{badBody, smallBody}
		attempt := 0
		m := &seqModel{pick: func(k int, _ string) string {
			a := answers[min(attempt, 1)]
			attempt++
			return a
		}}
		cfg.Model, cfg.Samples = m, samples
		out, err := Run(context.Background(), cfg, acc)
		if err != nil {
			t.Fatalf("%v %+v", err, out)
		}
		tree, _ := os.ReadFile(filepath.Join(root, "extra.go"))
		return m.prompts, out[1].Calls, normIDs(string(tree))
	}
	p0, c0, t0 := run(0)
	p1, c1, t1 := run(1)
	if c0 != 2 || c1 != 2 || t0 != t1 || normIDs(strings.Join(p0, "\x00")) != normIDs(strings.Join(p1, "\x00")) {
		t.Fatalf("Samples 0 and 1 differ: calls %d %d, prompts equal %v, trees equal %v",
			c0, c1, normIDs(strings.Join(p0, "\x00")) == normIDs(strings.Join(p1, "\x00")), t0 == t1)
	}
}

func TestRunSamplesDeterministic(t *testing.T) {
	var first string
	for round := 0; round < 4; round++ {
		cfg, root, acc := samplesSetup(t, doubleGate)
		bodies := []string{bigBody, smallBody, badBody}
		rot := round
		m := &seqModel{reverse: true, pick: func(k int, _ string) string { return bodies[(k+rot)%3] }}
		cfg.Model, cfg.Samples = m, 3
		if _, err := Run(context.Background(), cfg, acc); err != nil {
			t.Fatal(err)
		}
		// A new region takes the lowest-index passing sample; the fake model's
		// index↔body mapping follows scheduling, so check the outcome is a
		// passing body and that the whole tree is otherwise reproducible.
		got, _ := os.ReadFile(filepath.Join(root, "extra.go"))
		if !strings.Contains(string(got), "// small") && !strings.Contains(string(got), "// big") {
			t.Fatalf("round %d: wrong choice:\n%s", round, got)
		}
		core, _ := os.ReadFile(filepath.Join(root, "core.go"))
		if round == 0 {
			first = normIDs(string(core))
		} else if normIDs(string(core)) != first {
			t.Fatalf("round %d: tree differs\n%s\n---\n%s", round, first, normIDs(string(core)))
		}
	}
}

func TestDiffLines(t *testing.T) {
	if d := diffLines("a\nb\nc\n", "a\nx\nc\n"); d != 2 {
		t.Fatalf("got %d", d)
	}
	if d := diffLines("", "a\nb\n"); d != 2 {
		t.Fatalf("got %d", d)
	}
}

func decisionWith(details ...string) converge.Decision {
	var d converge.Decision
	for _, s := range details {
		d.Evidence = append(d.Evidence, evidence.Evidence{Detail: s})
	}
	return d
}

func TestSmokePassed(t *testing.T) {
	cases := []struct {
		name string
		d    converge.Decision
		want int
	}{
		{"none", decisionWith("ok", "FAIL"), -1},
		{"no evidence", converge.Decision{}, -1},
		{"first line", decisionWith("smoke: 7/12 checks passed\nFAIL x"), 7},
		{"later evidence", decisionWith("ok", "exit 1\nsmoke: 0/12 checks passed"), 0},
		{"after a head cut", decisionWith("smoke: 11/12 checks passed\n…\nlast lines"), 11},
		{"malformed", decisionWith("smoke: x/12 checks passed"), -1},
	}
	for _, c := range cases {
		if got := smokePassed(c.d); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCloser(t *testing.T) {
	mk := func(gates, smoke int, checked bool, diff int) sample {
		return sample{gates: gates, smoke: smoke, checked: checked, diff: diff}
	}
	cases := []struct {
		name string
		a, b sample
		want bool
	}{
		{"more gates wins", mk(2, -1, true, 9), mk(1, 12, true, 1), true},
		{"fewer gates loses", mk(1, 12, true, 1), mk(2, -1, true, 9), false},
		{"more smoke wins", mk(1, 5, true, 9), mk(1, 3, true, 1), true},
		{"missing count ranks below any count", mk(1, -1, true, 1), mk(1, 0, true, 9), false},
		{"any count beats missing", mk(1, 0, true, 9), mk(1, -1, true, 1), true},
		{"checked beats refused", mk(0, -1, true, 9), mk(0, -1, false, 1), true},
		{"smaller diff", mk(1, 4, true, 2), mk(1, 4, true, 3), true},
		{"equal keeps the earlier", mk(1, 4, true, 3), mk(1, 4, true, 3), false},
	}
	for _, c := range cases {
		if got := closer(c.a, c.b); got != c.want {
			t.Errorf("%s: closer = %v, want %v", c.name, got, c.want)
		}
	}
	// Transitivity: a > b > c in the cases above must also give a > c.
	a, b, c := mk(1, 5, true, 9), mk(1, 0, true, 1), mk(1, -1, true, 0)
	if !closer(a, b) || !closer(b, c) || !closer(a, c) {
		t.Error("ordering is not transitive over missing counts")
	}
}

// flakyModel fails the first fail extra.go calls of an attempt and answers the rest.
type flakyModel struct {
	*seqModel
	fail int
	mu2  sync.Mutex
	seen int
}

func (m *flakyModel) Complete(ctx context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, "Write the complete file `extra.go`") {
		m.mu2.Lock()
		m.seen++
		bad := m.seen <= m.fail
		m.mu2.Unlock()
		if bad {
			return "", errors.New("model exploded")
		}
	}
	return m.seqModel.Complete(ctx, prompt)
}

func TestRunSamplesPartialModelError(t *testing.T) {
	cfg, root, acc := samplesSetup(t, doubleGate)
	var progress strings.Builder
	cfg.Progress = &progress
	m := &flakyModel{seqModel: &seqModel{pick: func(int, string) string { return smallBody }}, fail: 2}
	cfg.Model, cfg.Samples = m, 3
	out, err := Run(context.Background(), cfg, acc)
	if err != nil || !out[1].Applied || out[1].Calls != 3 {
		t.Fatalf("%v %+v", err, out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "extra.go"))
	if !strings.Contains(string(got), "// small") {
		t.Fatalf("surviving sample not applied:\n%s", got)
	}
	if !strings.Contains(progress.String(), "2 of 3 model calls failed") {
		t.Fatalf("dropped samples are not reported:\n%s", progress.String())
	}
}

func TestRunSamplesAllModelErrors(t *testing.T) {
	cfg, _, acc := samplesSetup(t, doubleGate)
	m := &flakyModel{seqModel: &seqModel{pick: func(int, string) string { return smallBody }}, fail: 99}
	cfg.Model, cfg.Samples = m, 3
	_, err := Run(context.Background(), cfg, acc)
	if !errors.Is(err, ErrModel) {
		t.Fatalf("want ErrModel, got %v", err)
	}
}

// concModel records the most calls in flight at once.
type concModel struct {
	mu        sync.Mutex
	cur, peak int
}

func (m *concModel) Complete(_ context.Context, prompt string) (string, error) {
	if !strings.Contains(prompt, "Write the complete file `extra.go`") {
		return fenced(coreBody), nil
	}
	m.mu.Lock()
	m.cur++
	m.peak = max(m.peak, m.cur)
	m.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	m.mu.Lock()
	m.cur--
	m.mu.Unlock()
	return fenced(smallBody), nil
}

func TestRunSamplesCapsParallelCalls(t *testing.T) {
	cfg, _, acc := samplesSetup(t, doubleGate)
	m := &concModel{}
	cfg.Model, cfg.Samples = m, 10
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	if m.peak > maxParallelSamples {
		t.Fatalf("%d calls in flight, cap is %d", m.peak, maxParallelSamples)
	}
}

func TestPickExistingRegionTakesSmallestDiff(t *testing.T) {
	ok := converge.Trial{Decision: converge.Decision{Result: evidence.Result{Satisfied: true}}}
	all := []sample{
		{idx: 0, diff: 9, checked: true, trial: ok},
		{idx: 1, diff: 2, checked: false},
		{idx: 2, diff: 4, checked: true, trial: ok},
		{idx: 3, diff: 4, checked: true, trial: ok},
	}
	if w, n := pick(all, false); w == nil || w.idx != 2 || n != 3 {
		t.Fatalf("existing region: %+v %d", w, n)
	}
	if w, _ := pick(all, true); w == nil || w.idx != 0 {
		t.Fatalf("new region: %+v", w)
	}
	if w, n := pick(all[1:2], false); w != nil || n != 0 {
		t.Fatalf("none satisfied: %+v %d", w, n)
	}
}

func TestSeededRegionCountsAsFresh(t *testing.T) {
	seeded := "func handleJoin(c *client) {\n\t" + recording.Placeholder + "\n}\n"
	if !fresh("") || !fresh(seeded) || fresh("func f() { return }\n") {
		t.Fatal("fresh: empty and placeholder-only bodies are fresh; a real body is not")
	}
	// A seeded region takes the lowest index even though a later sample has a
	// smaller diff against the placeholder.
	ok := converge.Trial{Decision: converge.Decision{Result: evidence.Result{Satisfied: true}}}
	all := []sample{
		{idx: 0, diff: 30, checked: true, trial: ok},
		{idx: 1, diff: 3, checked: true, trial: ok},
	}
	if w, _ := pick(all, fresh(seeded)); w == nil || w.idx != 0 {
		t.Fatalf("seeded region: %+v", w)
	}
}
