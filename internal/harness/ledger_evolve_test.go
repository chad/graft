package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chad/graft/internal/bench"
)

// The dry path of `graft bench run --case examples/ledger-lite/case.json
// --evolve`: the baseline arm drives EvolveSample with a fake model over the
// real ledger-lite case files and the real held-out oracle. The "model" answers
// the build with the v1 reference and the change with a canned v2 server.

const llDir = "../../examples/ledger-lite/"

type ledgerModel struct{ v1, v2 string }

func (m ledgerModel) Complete(_ context.Context, prompt string) (string, error) {
	src := m.v1
	if strings.Contains(prompt, "revised spec") {
		src = m.v2
	}
	return "```go\n" + src + "```\n", nil
}

func ledgerCase(t *testing.T, v2 string) (Config, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	if testing.Short() {
		t.Skip("short")
	}
	read := func(rel string) string {
		b, err := os.ReadFile(llDir + rel)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	oracle := filepath.Join(t.TempDir(), "oracle")
	if out, err := exec.Command("go", "build", "-o", oracle, llDir+"oracle").CombinedOutput(); err != nil {
		t.Fatalf("build oracle: %v\n%s", err, out)
	}
	c := Config{
		Case: Case{Name: "ledger-lite-evolve", Spec: read("spec/ledger-lite.md"), SpecV2: read("spec-v2/ledger-lite.md"),
			Intent: "ledger", Contract: "It is a single Go program, standard library only.", Conventions: read("conventions.md"),
			Module: "ledgerlite", Fixture: "fx", OracleArgs: []string{"-v2"}},
		Model: ledgerModel{v1: read("reference/main.go"), v2: v2}, ModelName: "fake", Tier: "low", Attempts: 1,
		OracleArgv: []string{oracle}, Commit: "c1",
	}
	return c, read("reference-v2/main.go")
}

func TestLedgerEvolveDryPathScoresV2Reference(t *testing.T) {
	c, ref2 := ledgerCase(t, "")
	c.Model = ledgerModel{v1: c.Model.(ledgerModel).v1, v2: ref2}
	s := EvolveSample(context.Background(), c, bench.ArmBaseline)
	if s.Outcome != bench.Works || s.Passed != s.Checks || s.Checks < 50 {
		t.Fatalf("v2 reference under -v2: %+v", s)
	}
	if s.ChangeCalls == nil || *s.ChangeCalls != 1 {
		t.Fatalf("change calls %v", deref(s.ChangeCalls))
	}
	if s.PreChecks == nil || *s.PreChecks != 42 || *s.PrePassed != 42 || s.PreReason != "" {
		t.Fatalf("step-1 score: %v/%v %q, want 42/42", deref(s.PrePassed), deref(s.PreChecks), s.PreReason)
	}
}

// A change that does only the journal half: balance is added, but a frozen
// account still receives. The score names exactly the freezing checks.
func TestLedgerEvolveDryPathNamesTheMissedHalf(t *testing.T) {
	c, ref2 := ledgerCase(t, "")
	half := ref2
	for _, cut := range []string{
		"\t\tif err := l.creditable(a); err != nil {\n\t\t\treturn nil, err\n\t\t}\n",
		"\tif err := l.creditable(b); err != nil {\n\t\treturn 0, nil, err\n\t}\n",
	} {
		if !strings.Contains(half, cut) {
			t.Fatalf("anchor missing: %q", cut)
		}
		half = strings.Replace(half, cut, "", 1)
	}
	c.Model = ledgerModel{v1: c.Model.(ledgerModel).v1, v2: half}
	s := EvolveSample(context.Background(), c, bench.ArmBaseline)
	if s.Outcome != bench.Disagreed {
		t.Fatalf("%+v", s)
	}
	for _, name := range []string{"frozen_refuses_incoming", "frozen_both_directions"} {
		if !strings.Contains(s.Reason, name) {
			t.Errorf("reason %q does not name %s", s.Reason, name)
		}
	}
	for _, name := range []string{"journal_entries_carry_balance", "journal_balance_chain"} {
		if strings.Contains(s.Reason, name) {
			t.Errorf("reason %q names %s, which the half change satisfies", s.Reason, name)
		}
	}
	if s.PrePassed == nil || *s.PrePassed != 42 {
		t.Fatalf("step 1 should score 42/42: %+v", s)
	}
}

// A model that never changes anything (answers the old program again) fails the
// v2 checks, passes none of the new ones, and its step-1 score is still 42/42,
// so "failed to change" is not confused with "built it wrong".
func TestLedgerEvolveUnchangedProgramFailsOnlyV2Checks(t *testing.T) {
	c, _ := ledgerCase(t, "")
	v1 := c.Model.(ledgerModel).v1
	c.Model = ledgerModel{v1: v1, v2: v1}
	s := EvolveSample(context.Background(), c, bench.ArmBaseline)
	if s.Outcome != bench.Disagreed || s.PrePassed == nil || *s.PrePassed != 42 {
		t.Fatalf("%+v", s)
	}
	if strings.Contains(s.Reason, "deposit_adds") || strings.Contains(s.Reason, "transfer_moves_money") {
		t.Fatalf("a v1 check fails on the unchanged v1 program: %s", s.Reason)
	}
}
