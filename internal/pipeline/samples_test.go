package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
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

func TestRunSamplesPicksSmallestPassing(t *testing.T) {
	cfg, root, acc := samplesSetup(t, doubleGate)
	bodies := []string{bigBody, badBody, smallBody}
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
	if !strings.Contains(string(got), "// small") || strings.Contains(string(got), "// big") {
		t.Fatalf("the smaller passing body was not applied:\n%s", got)
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
		return m.prompts, out[1].Calls, string(tree)
	}
	p0, c0, t0 := run(0)
	p1, c1, t1 := run(1)
	if c0 != 2 || c1 != 2 || t0 != t1 || strings.Join(p0, "\x00") != strings.Join(p1, "\x00") {
		t.Fatalf("Samples 0 and 1 differ: calls %d %d, prompts equal %v, trees equal %v",
			c0, c1, strings.Join(p0, "\x00") == strings.Join(p1, "\x00"), t0 == t1)
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
		got, _ := os.ReadFile(filepath.Join(root, "extra.go"))
		if !strings.Contains(string(got), "// small") {
			t.Fatalf("round %d: wrong choice:\n%s", round, got)
		}
		if round == 0 {
			first = string(got)
		} else if string(got) != first {
			t.Fatalf("round %d: tree differs", round)
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
