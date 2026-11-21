package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/recording"
)

const dsSpec = "# app\n\n## One\n\n- The server must accept a nick\n\n## Two\n\n- The server must answer a PING with a PONG\n"

const dsGoodAnswer = "```json\n" + `{"files":[
 {"path":"one.go","section":"app › One","decls":[{"kind":"const","name":"limit","code":"const limit = 16"}],
  "stubs":[{"name":"main","doc":"parses flags and serves"},{"name":"accept","params":[{"name":"nick","type":"string"}],"results":["bool"],"doc":"accepts a nick of at most limit characters"}]},
 {"path":"two.go","section":"app › Two","stubs":[{"name":"ping","params":[{"name":"token","type":"string"}],"results":["string"],"doc":"answers PING"}]}
],"owners":{"S1":["one.go#accept"],"S2":["two.go#ping"]}}` + "\n```\n"

// dsSetup writes a config, spec and a canned model answer, and skips when the
// environment cannot render.
func dsSetup(t *testing.T, answer string) (cfg, spec, ans, out string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	if _, err := exec.LookPath("sedum"); err != nil || os.Getenv("SEDUM_GENERATORS") == "" {
		t.Skip("needs sedum on PATH and SEDUM_GENERATORS")
	}
	d := t.TempDir()
	cfg, spec, ans, out = filepath.Join(d, "graft.json"), filepath.Join(d, "spec.md"), filepath.Join(d, "answer.txt"), filepath.Join(d, "proposal.json")
	for p, c := range map[string]string{
		cfg:                         `{"module":"app","tier":"low","policy":{"low":["static_analysis"]},"gates":[],"conventions":"conv.md"}`,
		filepath.Join(d, "conv.md"): "Go, stdlib only, package main.\n",
		spec:                        dsSpec,
		ans:                         answer,
	} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestDraftSkeletonWritesAProposalAndAppliesNothing(t *testing.T) {
	cfg, spec, ans, out := dsSetup(t, dsGoodAnswer)
	var so, se bytes.Buffer
	code := run([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--model", "cat " + ans}, &so, &se)
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
	}
	for _, want := range []string{"model: cat ", "1 model call(s)", "the model's claim", "UNOWNED: none", "nothing applied"} {
		if !strings.Contains(so.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, so.String())
		}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recording.LoadSkeleton(b); err != nil {
		t.Fatalf("the proposal is not importable skeleton JSON: %v", err)
	}
	if _, err := os.Stat(out + ".coverage.json"); err != nil {
		t.Error(err)
	}
	// It refuses to overwrite without --force.
	so.Reset()
	se.Reset()
	if code := run([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--model", "cat " + ans}, &so, &se); code != 2 || !strings.Contains(se.String(), "--force") {
		t.Errorf("overwrite: exit %d, stderr %q", code, se.String())
	}
}

func TestDraftSkeletonUnownedExits4ButWritesTheProposal(t *testing.T) {
	ans := strings.Replace(dsGoodAnswer, `"S2":["two.go#ping"]`, `"S9":[]`, 1)
	ans = strings.Replace(ans, `"S9":[]`, `"S1":["one.go#accept"]`, 1) // S2 unowned
	ans = strings.Replace(ans, `"S1":["one.go#accept"],"S1":["one.go#accept"]`, `"S1":["one.go#accept"]`, 1)
	cfg, spec, a, out := dsSetup(t, ans)
	var so, se bytes.Buffer
	code := run([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--model", "cat " + a}, &so, &se)
	if code != 4 {
		t.Fatalf("exit %d, want 4\n%s\n%s", code, so.String(), se.String())
	}
	if !strings.Contains(so.String(), "-> UNOWNED") || !strings.Contains(so.String(), "UNOWNED: 1 of 2") {
		t.Errorf("report:\n%s", so.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Error("the proposal must still be written for review")
	}
}

// An import keeps its decls verbatim (freeq-lite has a full func main), but the
// same skeleton offered as a draft is rejected: nothing is written.
func TestDraftSkeletonRejectsLogicInDecls(t *testing.T) {
	ans := "```json\n" + `{"files":[
 {"path":"one.go","section":"app › One","decls":[{"kind":"func","name":"main","code":"func main() {}"}],"stubs":[{"name":"accept","doc":"d"}]},
 {"path":"two.go","section":"app › Two","stubs":[{"name":"ping","doc":"d"}]}],"owners":{}}` + "\n```\n"
	cfg, spec, a, out := dsSetup(t, ans)
	var so, se bytes.Buffer
	code := run([]string{"draft", "--skeleton", "--retries", "1", "--config", cfg, "--spec", spec, "--out", out, "--model", "cat " + a}, &so, &se)
	if code != 1 || !strings.Contains(so.String(), "2 model call(s)") || !strings.Contains(se.String(), "nothing written") {
		t.Fatalf("exit %d\n%s\n%s", code, so.String(), se.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a rejected proposal must not be written")
	}
}

func TestDraftSkeletonUsage(t *testing.T) {
	var so, se bytes.Buffer
	if code := run([]string{"draft", "--skeleton"}, &so, &se); code != 2 || !strings.Contains(se.String(), "exit codes") || !strings.Contains(se.String(), "UNOWNED") {
		t.Errorf("exit %d: %s", code, se.String())
	}
}

// draftWith runs the command against a canned answer and returns exit, stdout, stderr.
func draftWith(t *testing.T, answer string, extra ...string) (int, string, string) {
	t.Helper()
	cfg, spec, a, out := dsSetup(t, answer)
	args := append([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--model", "cat " + a}, extra...)
	var so, se bytes.Buffer
	code := run(args, &so, &se)
	if code != 0 && code != 4 {
		if _, err := os.Stat(out); err == nil {
			t.Error("a failed draft must not write the proposal")
		}
	}
	return code, so.String(), se.String()
}

// The real decl_check and the real build gate sit in this path: replacing
// either with a no-op makes these fail.
func TestDraftSkeletonRealDeclCheckRejectsLogic(t *testing.T) {
	ans := strings.Replace(dsGoodAnswer, `"decls":[{"kind":"const","name":"limit","code":"const limit = 16"}]`, `"decls":[{"kind":"var","name":"limit","code":"var limit = compute()"}]`, 1)
	code, so, se := draftWith(t, ans, "--retries", "0")
	if code != 1 || !strings.Contains(se, "decl_check rejected") || !strings.Contains(se, "one.go: limit:") {
		t.Fatalf("exit %d\n%s\n%s", code, so, se)
	}
}

func TestDraftSkeletonRealBuildGateRejectsATreeThatDoesNotBuild(t *testing.T) {
	ans := strings.Replace(dsGoodAnswer, `{"name":"nick","type":"string"}`, `{"name":"nick","type":"NoSuchType"}`, 1)
	code, so, se := draftWith(t, ans, "--retries", "0")
	if code != 1 || !strings.Contains(se, "does not build") || !strings.Contains(se, "NoSuchType") {
		t.Fatalf("exit %d\n%s\n%s", code, so, se)
	}
}

func TestDraftSkeletonDanglingClaimExits4(t *testing.T) {
	ans := strings.Replace(dsGoodAnswer, `"S1":["one.go#accept"]`, `"S1":["one.go#accept","one.go#nosuch"]`, 1)
	code, so, _ := draftWith(t, ans)
	if code != 4 || !strings.Contains(so, "DANGLING") || strings.Contains(so, "UNOWNED: ") && !strings.Contains(so, "UNOWNED: none") {
		t.Fatalf("exit %d\n%s", code, so)
	}
}

func TestDraftSkeletonZeroRetriesMakesOneCall(t *testing.T) {
	code, so, _ := draftWith(t, "not json at all", "--retries", "0")
	if code != 1 || !strings.Contains(so, "1 model call(s)") {
		t.Fatalf("exit %d\n%s", code, so)
	}
}

func TestDraftSkeletonRefusesFilesInTheConfig(t *testing.T) {
	cfg, spec, a, out := dsSetup(t, dsGoodAnswer)
	b, _ := os.ReadFile(cfg)
	b = bytes.Replace(b, []byte(`"gates":[]`), []byte(`"gates":[],"files":{"One":"pkg/one.go"}`), 1)
	if err := os.WriteFile(cfg, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	code := run([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--model", "cat " + a}, &so, &se)
	if code != 2 || !strings.Contains(se.String(), `"files" has no effect`) || strings.Contains(so.String(), "model:") {
		t.Fatalf("exit %d\n%s\n%s", code, so.String(), se.String())
	}
}

// A record's file is its affected_scope (since #36): a section accepted with
// files {"One": "pkg/one.go"} is drafted at pkg/one.go, never at the derived
// one.go, and a skeleton at the derived path is refused.
func TestDraftSkeletonPlacesAtTheRecordsFile(t *testing.T) {
	// pkg/one.go is a library package; the entry point lives in the root file.
	atPkg := strings.Replace(dsGoodAnswer, `"path":"one.go"`, `"path":"pkg/one.go"`, 1)
	atPkg = strings.Replace(atPkg, `{"name":"main","doc":"parses flags and serves"},`, "", 1)
	atPkg = strings.Replace(atPkg, `"one.go#accept"`, `"pkg/one.go#accept"`, 1)
	atPkg = strings.Replace(atPkg, `"stubs":[{"name":"ping"`, `"stubs":[{"name":"main","doc":"serves"},{"name":"ping"`, 1)
	cfg, spec, a, out := dsSetup(t, atPkg)
	repo := t.TempDir()
	if _, err := pipeline.AcceptWith(repo, drafter.Drafts(dsSpec), "t", time.Now(), pipeline.AcceptOptions{Ext: ".go", Files: map[string]string{"One": "pkg/one.go"}}); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	args := []string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--records", repo, "--model", "cat " + a}
	if code := run(args, &so, &se); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, so.String(), se.String())
	}
	b, _ := os.ReadFile(out)
	sk, err := recording.LoadSkeleton(b)
	if err != nil || sk.Files[0].Path != "pkg/one.go" {
		t.Fatalf("%v %+v", err, sk.Files)
	}
	// the prompt names the record's file, and the derived one is refused
	if err := os.WriteFile(a, []byte(dsGoodAnswer), 0o644); err != nil { // answer at one.go
		t.Fatal(err)
	}
	so.Reset()
	se.Reset()
	args = append(args, "--force", "--retries", "0")
	if code := run(args, &so, &se); code != 1 || !strings.Contains(se.String(), `exactly the file "pkg/one.go"`) {
		t.Fatalf("derived path with a record at pkg/one.go: exit %d\n%s\n%s", code, so.String(), se.String())
	}
	// a repo whose records sit at the derived path is fine with the derived path
	repo2 := t.TempDir()
	if _, err := pipeline.Accept(repo2, drafter.Drafts(dsSpec), "t", time.Now()); err != nil {
		t.Fatal(err)
	}
	so.Reset()
	se.Reset()
	args = []string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", out, "--records", repo2, "--force", "--model", "cat " + a}
	if code := run(args, &so, &se); code != 0 {
		t.Fatalf("agreeing repo: exit %d\n%s\n%s", code, so.String(), se.String())
	}
}
