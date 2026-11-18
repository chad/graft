package harness

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/bench"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
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
