package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/bench"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/recording"
)

const good = "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"

// answer returns code for any prompt; per-file answers for the graft arm.
type answer struct{ byFile map[string]string }

func (a answer) Complete(_ context.Context, prompt string) (string, error) {
	for f, code := range a.byFile {
		if strings.Contains(prompt, "Write the complete file `"+f+"`") {
			return "```go\n" + code + "```\n", nil
		}
	}
	return "```go\n" + a.byFile["main.go"] + "```\n", nil
}

// fakeOracle runs the server and reports 2 checks: prints hello / exits 0.
const fakeOracle = `bin="$2"; out=$("$bin"); p=0; [ "$out" = hello ] && p=$((p+1)); p=$((p+1)); f='[]'; [ $p -lt 2 ] && f='["hello"]'; echo "{\"checks\":2,\"passed\":$p,\"failed\":$f}"; [ $p -eq 2 ]`

func cfg(t *testing.T, m answer, oracle string) Config {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	return Config{
		Case: Case{Name: "tiny", Spec: "# Tiny\n\n## Core\n\n- The program must print \"hello\" when run\n",
			Intent: "Write a program that prints hello.", Contract: "It is run with no arguments.", Conventions: "package main", Module: "tiny", Fixture: "fx"},
		Model: m, ModelName: "fake", Tier: "low", Attempts: 1,
		Gates:      []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.TestRun, Timeout: time.Minute}},
		Policy:     evidence.Policy{"low": {evidence.TestRun}},
		OracleArgv: []string{"sh", "-c", oracle, "oracle"},
		Commit:     "c1",
	}
}

func TestOneCallArmsScore(t *testing.T) {
	ctx := context.Background()
	if s := Sample(ctx, cfg(t, answer{map[string]string{"main.go": good}}, fakeOracle), bench.ArmBaseline); s.Outcome != bench.Works || s.Passed != 2 {
		t.Fatalf("good baseline: %+v", s)
	}
	quiet := "package main\n\nfunc main() {}\n"
	if s := Sample(ctx, cfg(t, answer{map[string]string{"main.go": quiet}}, fakeOracle), bench.ArmIntent); s.Outcome != bench.Disagreed || s.Passed != 1 {
		t.Fatalf("disagreeing intent: %+v", s)
	}
	if s := Sample(ctx, cfg(t, answer{map[string]string{"main.go": "package main\nfunc main( {}\n"}}, fakeOracle), bench.ArmBaseline); s.Outcome != bench.Broke {
		t.Fatalf("non-building baseline: %+v", s)
	}
}

func TestOracleExitCodesMap(t *testing.T) {
	ctx := context.Background()
	m := answer{map[string]string{"main.go": good}}
	if s := Sample(ctx, cfg(t, m, `echo "never listened" >&2; exit 2`), bench.ArmBaseline); s.Outcome != bench.Broke {
		t.Fatalf("exit 2: %+v", s)
	}
	if s := Sample(ctx, cfg(t, m, `echo garbage; exit 1`), bench.ArmBaseline); s.Outcome != bench.Unreachable {
		t.Fatalf("unreadable oracle: %+v", s)
	}
}

func TestGraftArmRunsThePipeline(t *testing.T) {
	s := Sample(context.Background(), cfg(t, answer{map[string]string{"core.go": good}}, fakeOracle), bench.ArmGraft)
	if s.Outcome != bench.Works {
		t.Fatalf("%+v", s)
	}
}

func TestRunRecordsEveryArmAndReports(t *testing.T) {
	c := cfg(t, answer{map[string]string{"main.go": good, "core.go": good}}, fakeOracle)
	store := bench.Store{Dir: t.TempDir()}
	recs, err := Run(context.Background(), c, store, []bench.Arm{bench.ArmGraft, bench.ArmBaseline}, 2, bench.Smoke)
	if err != nil || len(recs) != 2 || len(recs[0].Samples) != 2 {
		t.Fatalf("%v %+v", err, recs)
	}
	got, bad, _ := store.Load("tiny")
	var b bytes.Buffer
	if err := bench.Report(&b, "tiny", got, nil, bad); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "smoke run") {
		t.Fatalf("smoke runs must be shown and counted in nothing:\n%s", b.String())
	}
}

func TestDigestSeparatesParts(t *testing.T) {
	if Digest("ab", "c") == Digest("a", "bc") {
		t.Fatal("digest is ambiguous across part boundaries")
	}
}

type failing struct{}

func (failing) Complete(context.Context, string) (string, error) {
	return "", errors.New("model CLI exploded")
}

// Review on #13: a model/CLI failure is infrastructure (unreachable), not
// broken code.
func TestModelFailureIsUnreachableNotBroke(t *testing.T) {
	c := cfg(t, answer{}, fakeOracle)
	c.Model = failing{}
	for _, arm := range []bench.Arm{bench.ArmBaseline, bench.ArmGraft} {
		if s := Sample(context.Background(), c, arm); s.Outcome != bench.Unreachable {
			t.Errorf("%s: %+v", arm, s)
		}
	}
}

func TestRunKeepsCallsAndTranscripts(t *testing.T) {
	c := cfg(t, answer{map[string]string{"main.go": good, "core.go": good}}, fakeOracle)
	c.Transcripts = t.TempDir()
	recs, err := Run(context.Background(), c, bench.Store{Dir: t.TempDir()}, []bench.Arm{bench.ArmBaseline}, 1, bench.Smoke)
	if err != nil {
		t.Fatal(err)
	}
	s := recs[0].Samples[0]
	if s.Calls == nil || *s.Calls != 1 || s.Transcript == "" {
		t.Fatalf("%+v", s)
	}
	for _, f := range []string{"01.prompt.md", "01.answer.md"} {
		if _, err := os.Stat(filepath.Join(s.Transcript, f)); err != nil {
			t.Errorf("transcript lacks %s", f)
		}
	}
}

// failsThenErrs returns non-building code first, then fails as a command.
type failsThenErrs struct{ n int }

func (f *failsThenErrs) Complete(context.Context, string) (string, error) {
	f.n++
	if f.n == 1 {
		return "```go\npackage main\nfunc main( {}\n```\n", nil
	}
	return "", errors.New("model CLI exploded")
}

// Review on #13: a code failure followed by a failed retry call is broke,
// not unreachable.
func TestCodeFailureThenModelErrorIsBroke(t *testing.T) {
	c := cfg(t, answer{}, fakeOracle)
	c.Model = &failsThenErrs{}
	c.Attempts = 3
	s := Sample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Broke || !strings.Contains(s.Reason, "a gate had failed") {
		t.Fatalf("%+v", s)
	}
}

func TestEvolveSampleGraftPreservesAndCounts(t *testing.T) {
	c := cfg(t, answer{map[string]string{"core.go": good, "extra.go": "package main\n\nfunc Twice(x int) int { return 2 * x }\n"}}, fakeOracle)
	c.Case.SpecV2 = c.Case.Spec + "\n## Extra\n\n- The program must define a function Twice that returns twice its argument\n"
	s := EvolveSample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Works || s.ChangeCalls == nil || *s.ChangeCalls != 1 || s.Preserved == nil || *s.Preserved != 1 ||
		s.DiffLines == nil || *s.DiffLines != 5 { // extra.go: 3 body lines + 2 marker lines
		t.Fatalf("%+v calls=%v preserved=%v", s, deref(s.ChangeCalls), deref(s.Preserved))
	}
}

func TestEvolveSampleBaselineCountsOneCall(t *testing.T) {
	c := cfg(t, answer{map[string]string{"main.go": good}}, fakeOracle)
	c.Case.SpecV2 = c.Case.Spec + "\n- It must also exit with status 0\n"
	s := EvolveSample(context.Background(), c, bench.ArmBaseline)
	if s.Outcome != bench.Works || s.ChangeCalls == nil || *s.ChangeCalls != 1 || s.Preserved != nil {
		t.Fatalf("%+v", s)
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestLineDiff(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"x\ny\nz\n", "x\ny\nz\n", 0},
		{"x\ny\nz\n", "x\nY\nz\n", 2},
		{"", "a\nb\n", 2},
		{"a\nb\n", "", 2},
		{"a\nb\nc\n", "a\nc\n", 1},
	} {
		if got := lineDiff(c.a, c.b); got != c.want {
			t.Errorf("lineDiff(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	// A rewrite that changes one line in a big file is small, however it was produced.
	big := strings.Repeat("line\n", 500)
	if d := diffLines(map[string]string{"main.go": big}, map[string]string{"main.go": big + "extra\n"}); d != 1 {
		t.Fatalf("one added line counted as %d", d)
	}
}

// failsThenPassesThenErrs: region 1 fails then passes; region 2 hits a model error.
type failsThenPassesThenErrs struct{ n int }

func (f *failsThenPassesThenErrs) Complete(_ context.Context, prompt string) (string, error) {
	f.n++
	switch {
	case strings.Contains(prompt, "`core.go`") && f.n == 1:
		return "```go\npackage main\nfunc main( {}\n```\n", nil
	case strings.Contains(prompt, "`core.go`"):
		return "```go\n" + good + "```\n", nil
	}
	return "", errors.New("model CLI exploded")
}

// Review on #13: a gate failure anywhere in the sample (even in a region that
// later passed) makes a later infrastructure failure broke, not unreachable.
func TestEarlierGateFailureInAnyRegionIsBroke(t *testing.T) {
	c := cfg(t, answer{}, fakeOracle)
	c.Case.Spec += "\n## Extra\n\n- The program must define a function Twice that returns twice its argument\n"
	c.Model = &failsThenPassesThenErrs{}
	c.Attempts = 3
	s := Sample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Broke {
		t.Fatalf("%+v", s)
	}
}

// A gate failure followed by cancellation is broke.
func TestGateFailureThenCancelIsBroke(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := cfg(t, answer{}, fakeOracle)
	n := 0
	c.Model = modelFunc(func(context.Context, string) (string, error) {
		n++
		if n == 2 {
			cancel()
			return "", ctx.Err()
		}
		return "```go\npackage main\nfunc main( {}\n```\n", nil
	})
	c.Attempts = 3
	if s := Sample(ctx, c, bench.ArmGraft); s.Outcome != bench.Broke {
		t.Fatalf("%+v", s)
	}
}

type modelFunc func(context.Context, string) (string, error)

func (f modelFunc) Complete(ctx context.Context, p string) (string, error) { return f(ctx, p) }

// Review on #16: in an evolve sample, a gate retry during v1 followed by a
// model error during the change is unreachable (the change was never
// measured), not broke.
func TestEvolveV1RetryThenChangeInfraIsUnreachable(t *testing.T) {
	c := cfg(t, answer{}, fakeOracle)
	c.Case.SpecV2 = c.Case.Spec + "\n## Extra\n\n- The program must define a function Twice that returns twice its argument\n"
	n := 0
	c.Model = modelFunc(func(_ context.Context, prompt string) (string, error) {
		n++
		switch {
		case n == 1: // v1 core.go: fails the build gate
			return "```go\npackage main\nfunc main( {}\n```\n", nil
		case strings.Contains(prompt, "`core.go`"): // v1 core.go retry: passes
			return "```go\n" + good + "```\n", nil
		}
		return "", errors.New("model CLI exploded") // the change step
	})
	c.Attempts = 3
	if s := EvolveSample(context.Background(), c, bench.ArmGraft); s.Outcome != bench.Unreachable {
		t.Fatalf("%+v", s)
	}
}

func TestGraftRenderWithoutSedumIsUnreachable(t *testing.T) {
	c := cfg(t, answer{map[string]string{"core.go": good}}, fakeOracle)
	c.Case.Skeleton = &recording.Skeleton{}
	if s := Sample(context.Background(), c, bench.ArmGraftRender); s.Outcome != bench.Unreachable {
		t.Fatalf("%+v", s)
	}
}

func TestGraftRenderWithoutSkeletonIsUnreachable(t *testing.T) {
	c := cfg(t, answer{map[string]string{"core.go": good}}, fakeOracle)
	if s := Sample(context.Background(), c, bench.ArmGraftRender); s.Outcome != bench.Unreachable || !strings.Contains(s.Reason, "no skeleton") {
		t.Fatalf("%+v", s)
	}
}

func TestVisibleCountsAreRecorded(t *testing.T) {
	visOracle := `bin="$2"; "$bin" >/dev/null; echo '{"checks":2,"passed":2,"failed":[],"visible":1,"visible_passed":1}'`
	s := Sample(context.Background(), cfg(t, answer{map[string]string{"main.go": good}}, visOracle), bench.ArmBaseline)
	if s.Outcome != bench.Works || s.Visible == nil || *s.Visible != 1 || s.VisiblePassed == nil || *s.VisiblePassed != 1 {
		t.Fatalf("%+v", s)
	}
	if s := Sample(context.Background(), cfg(t, answer{map[string]string{"main.go": good}}, fakeOracle), bench.ArmBaseline); s.Visible != nil {
		t.Fatalf("an oracle without visible counts must leave them unset: %+v", s)
	}
}

func TestFilterSynthIsSuffixIndependent(t *testing.T) {
	m := map[string]string{
		"channels.go":       "// sedum:begin x\npackage main\n",
		"channels_synth.go": "// graft:synth:begin {}\npackage main\n// graft:synth:end {}\n",
		"channels.gen.ts":   "// graft:synth:begin {}\nexport {}\n// graft:synth:end {}\n",
	}
	got := filterSynth(m)
	if len(got) != 2 || got["channels.go"] != "" {
		t.Fatalf("%v", got)
	}
}
