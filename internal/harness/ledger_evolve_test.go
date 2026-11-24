package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/bench"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/crossref"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/render"
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

// ---- M14: the cross-reference step on the ledger-lite change (graft arm) ----

// hookModel is a fake model for the graft arm over the real ledger-lite case.
// Step 1 writes running.go as the v2 reference with the two credit checks (the
// deposit route's and the transfer route's) behind package-level hooks that do
// nothing; every other file is an empty package. A regenerated deposits or
// transfers file installs its hook (the frozen check) only when its prompt
// carries the cross-referenced frozen statement: i.e. only when the step reached it.
// xref answers the cross-reference prompt.
type hookModel struct {
	ref2    string
	xref    func(prompt string) string
	regen   map[string]bool // section -> its file was asked for with the cross-reference in its prompt
	prompts int
}

var (
	reRecord  = regexp.MustCompile(`(?s)### record (\S+) — ([^\n]+)\nfile: (\S+)\nintent:\n[^\n]*\n- \[\w+\] ([^\n]+)`)
	reStmtID  = regexp.MustCompile(`- id (s\d+), section "([^"]+)", \[\w+\]: ([^\n]+)`)
	reSection = regexp.MustCompile(`## Your section: ([^\n]+)`)
)

func lastPart(s string) string {
	if i := strings.LastIndex(s, " › "); i >= 0 {
		return s[i+len(" › "):]
	}
	return s
}

var reFromFreezing = regexp.MustCompile(`cross-reference: stated in "[^"]*Freezing"`)

const incomingFrozen = "refuse money coming in"

// correctXref answers like a model that understood the change: the Freezing
// statement about incoming money constrains Deposits and Transfers, the rest is local.
func correctXref(prompt string) string {
	recs := map[string][3]string{} // last part of the section title -> id, file, a quotable phrase of its intent
	for _, m := range reRecord.FindAllStringSubmatch(prompt, -1) {
		recs[lastPart(m[2])] = [3]string{m[1], m[3], m[4]}
	}
	var ents []string
	for _, m := range reStmtID.FindAllStringSubmatch(prompt, -1) {
		if !strings.Contains(m[3], incomingFrozen) {
			ents = append(ents, `{"id":"`+m[1]+`","verdict":"local"}`)
			continue
		}
		var ts []string
		for _, sec := range []string{"Deposits and withdrawals", "Transfers"} {
			r := recs[sec]
			q, _ := json.Marshal(r[2])
			ts = append(ts, `{"record":"`+r[0]+`","reason":"the credit side of this route must refuse a frozen account","location":"`+
				r[1]+`: main","quote":`+string(q)+`}`)
		}
		ents = append(ents, `{"id":"`+m[1]+`","verdict":"constrains","targets":[`+strings.Join(ts, ",")+`]}`)
	}
	return "```json\n{\"statements\":[" + strings.Join(ents, ",") + "]}\n```\n"
}

func (m *hookModel) Complete(_ context.Context, prompt string) (string, error) {
	m.prompts++
	if strings.Contains(prompt, crossref.Marker) {
		return m.xref(prompt), nil
	}
	g := regexp.MustCompile("Write the complete file `([^`]+)`").FindStringSubmatch(prompt)
	if g == nil {
		return "", os.ErrInvalid
	}
	sec := ""
	if s := reSection.FindStringSubmatch(prompt); s != nil {
		sec = lastPart(s[1])
	}
	if g[1] == "running.go" {
		src := strings.Replace(m.ref2, "l.creditable(a)", "depositHook(a)", 1)
		src = strings.Replace(src, "l.creditable(b)", "transferHook(b)", 1)
		src += "\nvar depositHook = func(a *account) error { return nil }\nvar transferHook = func(b *account) error { return nil }\n"
		return "```go\n" + src + "```\n", nil
	}
	if reFromFreezing.MatchString(prompt) {
		m.regen[sec] = true
		switch sec {
		case "Deposits and withdrawals":
			return "```go\npackage main\n\nfunc init() {\n\tdepositHook = func(a *account) error {\n\t\tif a.Frozen {\n\t\t\treturn errFrozen\n\t\t}\n\t\treturn nil\n\t}\n}\n```\n", nil
		case "Transfers":
			return "```go\npackage main\n\nfunc init() {\n\ttransferHook = func(b *account) error {\n\t\tif b.Frozen {\n\t\t\treturn errFrozen\n\t\t}\n\t\treturn nil\n\t}\n}\n```\n", nil
		}
	}
	return "```go\npackage main\n```\n", nil
}

func graftLedger(t *testing.T, xref func(string) string) (Config, *hookModel) {
	t.Helper()
	c, ref2 := ledgerCase(t, "")
	hm := &hookModel{ref2: ref2, xref: xref, regen: map[string]bool{}}
	c.Model = hm
	c.Gates = []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.TestRun, Timeout: 2 * time.Minute}}
	c.Policy = evidence.Policy{"low": {evidence.TestRun}}
	return c, hm
}

// The yardstick: with a correct cross-reference answer the frozen rule reaches
// the Deposits and Transfers regions, the change passes every -v2 check, and the
// change-cost record shows the cross-reference call.
func TestLedgerEvolveGraftArmReachesDepositsAndTransfersThroughCrossref(t *testing.T) {
	c, hm := graftLedger(t, correctXref)
	s := EvolveSample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Works || s.Passed != s.Checks || s.Checks < 51 {
		t.Fatalf("%+v regen=%v", s, hm.regen)
	}
	if !hm.regen["Deposits and withdrawals"] || !hm.regen["Transfers"] {
		t.Fatalf("the frozen statement did not reach the regions: %v", hm.regen)
	}
	if s.CrossrefCalls == nil || *s.CrossrefCalls != 1 || s.ChangeCalls == nil || *s.ChangeCalls < 5 {
		t.Fatalf("crossref calls %v, change calls %v", deref(s.CrossrefCalls), deref(s.ChangeCalls))
	}
}

// The limit, as a test: a well-formed but wrong "local" verdict is accepted
// (that is what printing every local verdict is for) and the dropped rule shows
// only in the oracle, exactly as before the step existed.
func TestLedgerEvolveWrongLocalVerdictStaysSilentUntilTheOracle(t *testing.T) {
	c, hm := graftLedger(t, localAnswer)
	s := EvolveSample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Disagreed || !strings.Contains(s.Reason, "frozen_refuses_incoming") {
		t.Fatalf("%+v", s)
	}
	if hm.regen["Deposits and withdrawals"] || hm.regen["Transfers"] {
		t.Fatalf("nothing should have reached them: %v", hm.regen)
	}
}

// A missing answer is loud: the accept refuses and nothing is generated.
func TestLedgerEvolveMissingAnswerRefusesTheChange(t *testing.T) {
	c, _ := graftLedger(t, func(string) string { return "```json\n{\"statements\":[]}\n```\n" })
	s := EvolveSample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Broke || !strings.Contains(s.Reason, "UNANSWERED") {
		t.Fatalf("%+v", s)
	}
}

// A dangling claim (an unknown record) is refused as loudly.
func TestLedgerEvolveDanglingAnswerRefusesTheChange(t *testing.T) {
	c, _ := graftLedger(t, func(p string) string {
		out := correctXref(p)
		return strings.NewReplacer(`"record":"prov-`, `"record":"nope-prov-`).Replace(out)
	})
	s := EvolveSample(context.Background(), c, bench.ArmGraft)
	if s.Outcome != bench.Broke || !strings.Contains(s.Reason, "DANGLING") {
		t.Fatalf("%+v", s)
	}
}

// ---- M14: the same, in render mode (graft-render arm) ----

// renderHookModel is the fake for the graft-render arm over the real skeletons.
// Running's region holds the v2 reference (with the two credit checks behind
// hooks that do nothing) and every stub of Running delegates to it; every
// other region's init body does nothing, except that a regenerated Deposits or
// Transfers region installs its hook (the frozen check) only when its prompt
// carries the cross-referenced statement.
type renderHookModel struct {
	ref2   string
	xref   func(prompt string) string
	events []string // "xref", or the region path asked for, in order
	// at the first region prompt after the cross-reference call:
	sawImports, sawCreditable bool
	armed                     bool
	tmp                       string
	regen                     map[string]bool
}

var (
	reAskedRegion = regexp.MustCompile("complete contents of `([^`]+)`")
	reDeclLine    = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(([^)]*)\)(?: (.+))?$`)
)

func zeroOf(t string) string {
	switch {
	case t == "bool":
		return "false"
	case t == "string":
		return `""`
	case strings.HasPrefix(t, "int") || strings.HasPrefix(t, "uint") || strings.HasPrefix(t, "float"):
		return "0"
	default:
		return "nil"
	}
}

func (m *renderHookModel) Complete(_ context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, crossref.Marker) {
		m.events = append(m.events, "xref")
		m.armed = true
		return m.xref(prompt), nil
	}
	g := reAskedRegion.FindStringSubmatch(prompt)
	if g == nil {
		return "", os.ErrInvalid
	}
	path := g[1]
	m.events = append(m.events, path)
	if m.armed { // first region prompt after the cross-reference call
		m.armed = false
		idx, _ := filepath.Glob(filepath.Join(m.tmp, "graft-evolve-*", "repo", ".graft", "accepted.json"))
		for _, f := range idx {
			if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), `"imports"`) {
				m.sawImports = true
			}
		}
		run, _ := filepath.Glob(filepath.Join(m.tmp, "graft-evolve-*", "app", "running.go"))
		for _, f := range run {
			if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "Creditable") {
				m.sawCreditable = true
			}
		}
	}
	decl := prompt[strings.Index(prompt, "## Declares"):]
	decl = decl[strings.Index(decl, "```go\n")+6:]
	decl = decl[:strings.Index(decl, "```")]
	var b strings.Builder
	if strings.HasPrefix(path, "running") {
		src := strings.Replace(m.ref2, "l.creditable(a)", "depositHook(a)", 1)
		src = strings.Replace(src, "l.creditable(b)", "transferHook(b)", 1)
		src = regexp.MustCompile(`(?s)func main\(\) \{.*?\n\}\n`).ReplaceAllString(src, "")
		src = strings.NewReplacer("\t\"flag\"\n", "", "\t\"log\"\n", "").Replace(src)
		b.WriteString(src)
		b.WriteString("\nvar depositHook = func(a *account) error { return nil }\nvar transferHook = func(b *account) error { return nil }\n")
		b.WriteString("\nvar refLedger = &ledger{accounts: map[string]*account{}, transfers: map[string]*transfer{}, byKey: map[string]*transfer{}, holds: map[string]*hold{}}\n")
	} else {
		b.WriteString("package main\n")
	}
	for _, d := range reDeclLine.FindAllStringSubmatch(decl, -1) {
		full := d[0]
		body := ""
		switch {
		case strings.HasSuffix(d[1], "ServeHTTPBody"):
			body = "refLedger.ServeHTTP(w, r)"
		case strings.HasPrefix(path, "deposits") && m.carries(prompt):
			body = "depositHook = func(a *account) error {\n\t\tif a.Frozen {\n\t\t\treturn errFrozen\n\t\t}\n\t\treturn nil\n\t}"
			m.regen["deposits"] = true
		case strings.HasPrefix(path, "transfers") && m.carries(prompt):
			body = "transferHook = func(b *account) error {\n\t\tif b.Frozen {\n\t\t\treturn errFrozen\n\t\t}\n\t\treturn nil\n\t}"
			m.regen["transfers"] = true
		case d[3] != "":
			rs := strings.Trim(d[3], "()")
			var zs []string
			for _, r := range strings.Split(rs, ",") {
				zs = append(zs, zeroOf(strings.TrimSpace(r)))
			}
			body = "return " + strings.Join(zs, ", ")
		}
		b.WriteString("\n" + full + " {\n\t" + body + "\n}\n")
	}
	return "```go\n" + b.String() + "```\n", nil
}

func (m *renderHookModel) carries(prompt string) bool { return reFromFreezing.MatchString(prompt) }

func renderLedger(t *testing.T, xref func(string) string) (Config, *renderHookModel) {
	t.Helper()
	bin, err := exec.LookPath("sedum")
	gens := os.Getenv("SEDUM_GENERATORS")
	if err != nil || gens == "" {
		t.Skip("needs sedum >= 0.14.0 on PATH and SEDUM_GENERATORS")
	}
	sed, err := render.Open(context.Background(), bin)
	if err != nil {
		t.Skip("needs sedum >= 0.14.0: " + err.Error())
	}
	c, ref2 := ledgerCase(t, "")
	load := func(rel string) *recording.Skeleton {
		b, err := os.ReadFile(llDir + rel)
		if err != nil {
			t.Fatal(err)
		}
		sk, err := recording.LoadSkeleton(b)
		if err != nil {
			t.Fatal(err)
		}
		return &sk
	}
	c.Case.Skeleton, c.Case.SkeletonV2 = load("skeleton.json"), load("skeleton-v2.json")
	c.Sedum, c.Generators = sed, gens
	c.Gates = []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.TestRun, Timeout: 2 * time.Minute}}
	c.Policy = evidence.Policy{"low": {evidence.TestRun}}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	m := &renderHookModel{ref2: ref2, xref: xref, tmp: tmp, regen: map[string]bool{}}
	c.Model = m
	return c, m
}

// renderXref answers like correctXref but names the stub the skeleton has for
// both targets: their init.
func renderXref(prompt string) string {
	return strings.ReplaceAll(correctXref(prompt), `.go: main"`, `.go: init"`)
}

// The graft-render arm: with a correct answer the imports reach the Deposits
// and Transfers regions and every -v2 check passes. The order that runs is: the
// cross-reference call (before anything is written), the accept that writes the
// imports into the records, the skeleton import and render (Running's new shape:
// Creditable), and only then the regions, so no region is ever generated from a
// stale record.
func TestLedgerEvolveRenderArmReachesDepositsAndTransfersThroughCrossref(t *testing.T) {
	c, m := renderLedger(t, renderXref)
	s := EvolveSample(context.Background(), c, bench.ArmGraftRender)
	if s.Outcome != bench.Works || s.Passed != s.Checks || s.Checks < 51 {
		t.Fatalf("%+v events=%v regen=%v", s, m.events, m.regen)
	}
	if !m.regen["deposits"] || !m.regen["transfers"] {
		t.Fatalf("the frozen statement did not reach the regions: %v", m.regen)
	}
	if s.CrossrefCalls == nil || *s.CrossrefCalls != 1 {
		t.Fatalf("crossref calls %v", deref(s.CrossrefCalls))
	}
	// step 2 begins with the one cross-reference call, and nothing else was asked before it in step 2
	x := -1
	for i, e := range m.events {
		if e == "xref" {
			x = i
		}
	}
	if x < 0 || x != 7 {
		t.Fatalf("the cross-reference call should follow step 1's 7 regions: events=%v", m.events)
	}
	if !m.sawImports || !m.sawCreditable {
		t.Fatalf("at the first region of the change the imports must be in the records (%v) and the new skeleton rendered (%v)", m.sawImports, m.sawCreditable)
	}
	got := map[string]bool{}
	for _, e := range m.events[x+1:] {
		got[strings.SplitN(e, "_synth", 2)[0]] = true
	}
	for _, want := range []string{"deposits_and_withdrawals", "transfers", "running", "freezing", "journal"} {
		if !got[want] {
			t.Errorf("the change did not regenerate %s: %v", want, m.events[x+1:])
		}
	}
}

// In render mode a location must name a stub or decl of the target's skeleton
// file; one that does not needs a skeleton change, and the change is refused.
func TestLedgerEvolveRenderArmRefusesALocationTheSkeletonLacks(t *testing.T) {
	c, _ := renderLedger(t, func(p string) string {
		return strings.ReplaceAll(correctXref(p), `.go: main"`, `.go: Deposit"`)
	})
	s := EvolveSample(context.Background(), c, bench.ArmGraftRender)
	if s.Outcome != bench.Broke || !strings.Contains(s.Reason, "DANGLING") || !strings.Contains(s.Reason, "needs a skeleton change (graft draft --skeleton)") {
		t.Fatalf("%+v", s)
	}
}

// Control: in render mode a "local" verdict leaves Deposits and Transfers alone
// and the frozen rule is dropped, visible only in the oracle.
func TestLedgerEvolveRenderArmWrongLocalVerdictFailsFrozenChecks(t *testing.T) {
	c, m := renderLedger(t, localAnswer)
	s := EvolveSample(context.Background(), c, bench.ArmGraftRender)
	if s.Outcome != bench.Disagreed || !strings.Contains(s.Reason, "frozen_refuses_incoming") || m.regen["deposits"] || m.regen["transfers"] {
		t.Fatalf("%+v regen=%v", s, m.regen)
	}
}
