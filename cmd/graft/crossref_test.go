package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cxSpec1 = "# T\n\n## Alpha\n\n- Alpha must record every event it sees\n\n## Beta\n\n- Beta must count the events it is told about\n"
const cxRule = "Every event must also reach the counters before the reply is sent"
const cxSpec2 = "# T\n\n## Alpha\n\n- Alpha must record every event it sees\n- " + cxRule + "\n\n## Beta\n\n- Beta must count the events it is told about\n"

type cxEnv struct {
	dir, cfg, repo, root string
}

// cxSetup writes a config whose "model" answers code prompts with an empty
// package, and returns the env. A cross-reference model is passed per call.
func cxSetup(t *testing.T) cxEnv {
	t.Helper()
	dir := t.TempDir()
	e := cxEnv{dir: dir, cfg: filepath.Join(dir, "graft.json"), repo: filepath.Join(dir, "repo"), root: filepath.Join(dir, "app")}
	os.WriteFile(filepath.Join(dir, "conv.md"), []byte("package main\n"), 0o644)
	code := filepath.Join(dir, "code.sh")
	os.WriteFile(code, []byte("#!/bin/sh\ncat >/dev/null\nprintf '```go\\npackage main\\n```\\n'\n"), 0o755)
	cfg := map[string]any{
		"module": "t", "tier": "low", "conventions": "conv.md", "attempts": 1,
		"model":  map[string]any{"argv": []string{code}, "timeout_sec": 30},
		"policy": map[string]any{"low": []string{"static_analysis"}},
		"gates":  []any{map[string]any{"label": "ok", "argv": []string{"true"}, "kind": "static_analysis"}},
	}
	b, _ := json.Marshal(cfg)
	os.WriteFile(e.cfg, b, 0o644)
	return e
}

func (e cxEnv) spec(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(e.dir, name)
	os.WriteFile(p, []byte(text), 0o644)
	return p
}

func (e cxEnv) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func (e cxEnv) runAccept(t *testing.T, spec string, extra ...string) (int, string, string) {
	args := append([]string{"run", "--config", e.cfg, "--spec", spec, "--repo", e.repo, "--root", e.root, "--accept"}, extra...)
	return e.run(t, args...)
}

// cxModel writes a model script answering the cross-reference prompt with body.
func (e cxEnv) cxModel(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(e.dir, name)
	os.WriteFile(p, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EOF'\n```json\n"+body+"\n```\nEOF\n"), 0o755)
	return p
}

func (e cxEnv) betaTarget(t *testing.T) (id, file string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.repo, ".graft", "accepted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Records map[string]struct{ Section, File string }
	}
	json.Unmarshal(b, &idx)
	for id, r := range idx.Records {
		if strings.HasSuffix(r.Section, "Beta") {
			return id, r.File
		}
	}
	t.Fatal("no Beta record")
	return "", ""
}

func TestRunRefusesChangedStatementsWithoutCrossref(t *testing.T) {
	e := cxSetup(t)
	s1, s2 := e.spec(t, "s1.md", cxSpec1), e.spec(t, "s2.md", cxSpec2)
	code, out, errb := e.runAccept(t, s1)
	if code != 0 || !strings.Contains(out, "cross-references: skipped") || !strings.Contains(out, "cross-references: 0 accepted into 0 record(s), 0 judged local, 0 unchecked") {
		t.Fatalf("first accept: exit %d\n%s\n%s", code, out, errb)
	}

	// the dry run says what is pending
	_, out, _ = e.run(t, "run", "--config", e.cfg, "--spec", s2, "--repo", e.repo, "--root", e.root)
	if !strings.Contains(out, "1 statement(s) are new or changed") {
		t.Fatalf("dry run:\n%s", out)
	}

	code, out, errb = e.runAccept(t, s2)
	if code != 2 || !strings.Contains(errb, "UNANSWERED") || !strings.Contains(errb, "--no-crossref") || strings.Contains(out, "regions applied") {
		t.Fatalf("exit %d, want 2 before generating\nout:%s\nerr:%s", code, out, errb)
	}

	// the waiver is loud
	code, out, errb = e.runAccept(t, s2, "--no-crossref")
	if code != 0 || !strings.Contains(out, "cross-references NOT checked: 1 changed statement(s)") || !strings.Contains(out, "1 unchecked") {
		t.Fatalf("waiver: exit %d\n%s\n%s", code, out, errb)
	}
}

func TestCrossrefProposesAcceptsAndSummarises(t *testing.T) {
	e := cxSetup(t)
	s1, s2 := e.spec(t, "s1.md", cxSpec1), e.spec(t, "s2.md", cxSpec2)
	if code, out, errb := e.runAccept(t, s1); code != 0 {
		t.Fatalf("%d %s %s", code, out, errb)
	}
	id, file := e.betaTarget(t)
	model := e.cxModel(t, "x.sh", `{"statements":[{"id":"s1","verdict":"constrains","targets":[{"record":"`+id+`","reason":"the rule binds Beta's counting code","location":"`+file+`: main","quote":"Beta must count the events"}]}]}`)
	out1 := filepath.Join(e.dir, "crossref.json")
	args := []string{"crossref", "--config", e.cfg, "--spec", s2, "--repo", e.repo, "--out", out1, "--model", model}
	code, out, errb := e.run(t, args...)
	if code != 0 || !strings.Contains(out, "1 model call(s)") || !strings.Contains(out, "constrains "+id) || !strings.Contains(out, "nothing applied") {
		t.Fatalf("crossref: exit %d\n%s\n%s", code, out, errb)
	}
	if _, err := os.Stat(out1); err != nil {
		t.Fatal("no proposal written")
	}
	if code, _, errb = e.run(t, args...); code != 2 || !strings.Contains(errb, "--force") {
		t.Fatalf("an existing --out must be refused: %d %s", code, errb)
	}
	// propose-only: no record changed
	if code, out, _ := e.runAccept(t, s2); code != 2 {
		t.Fatalf("still refused until --crossref: %d %s", code, out)
	}
	code, out, errb = e.runAccept(t, s2, "--crossref", out1)
	if code != 0 || !strings.Contains(out, "cross-reference accepted:") || !strings.Contains(out, "cross-references: 1 accepted into 1 record(s), 0 judged local, 0 unchecked") {
		t.Fatalf("accept: exit %d\n%s\n%s", code, out, errb)
	}
	if code, _, errb := e.runAccept(t, s2, "--crossref", out1, "--no-crossref"); code != 2 || !strings.Contains(errb, "exclusive") {
		t.Fatalf("%d %s", code, errb)
	}
}

func TestCrossrefUnansweredAndDanglingExitFour(t *testing.T) {
	e := cxSetup(t)
	s1, s2 := e.spec(t, "s1.md", cxSpec1), e.spec(t, "s2.md", cxSpec2)
	e.runAccept(t, s1)
	for name, body := range map[string]string{
		"unanswered": `{"statements":[]}`,
		"dangling":   `{"statements":[{"id":"s1","verdict":"constrains","targets":[{"record":"nope","reason":"r","location":"x.go: f","quote":"q"}]}]}`,
	} {
		model := e.cxModel(t, name+".sh", body)
		out1 := filepath.Join(e.dir, name+".json")
		code, out, _ := e.run(t, "crossref", "--config", e.cfg, "--spec", s2, "--repo", e.repo, "--out", out1, "--model", model)
		if code != 4 || !strings.Contains(out, strings.ToUpper(name)) {
			t.Fatalf("%s: exit %d\n%s", name, code, out)
		}
		if _, err := os.Stat(out1); err != nil {
			t.Fatalf("%s: the proposal is written so a human sees it", name)
		}
		if code, _, errb := e.runAccept(t, s2, "--crossref", out1); code != 2 || !strings.Contains(errb, strings.ToUpper(name)) {
			t.Fatalf("%s: accept exit %d %s", name, code, errb)
		}
	}
	// no parseable answer: exit 1, nothing written
	bad := e.cxModel(t, "bad.sh", "not json")
	out1 := filepath.Join(e.dir, "bad.json")
	if code, _, _ := e.run(t, "crossref", "--config", e.cfg, "--spec", s2, "--repo", e.repo, "--out", out1, "--model", bad, "--retries", "0"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if _, err := os.Stat(out1); err == nil {
		t.Fatal("nothing must be written")
	}
}

// The advisory warning is printed and never changes the exit code.
func TestCrossrefAdvisoryWarningNeverChangesTheExitCode(t *testing.T) {
	e := cxSetup(t)
	s1 := e.spec(t, "s1.md", "# T\n\n## Alpha\n\n- Alpha must answer 421 to anything else\n\n## Beta\n\n- Beta must handle `JOIN` and answer 332 then 353\n")
	s2 := e.spec(t, "s2.md", "# T\n\n## Alpha\n\n- Alpha must answer 421 to anything else\n- A joiner must receive 332 before 353\n\n## Beta\n\n- Beta must handle `JOIN` and answer 332 then 353\n")
	e.runAccept(t, s1)
	model := e.cxModel(t, "local.sh", `{"statements":[{"id":"s1","verdict":"local"}]}`)
	code, out, _ := e.run(t, "crossref", "--config", e.cfg, "--spec", s2, "--repo", e.repo, "--out", filepath.Join(e.dir, "l.json"), "--model", model)
	if code != 0 || !strings.Contains(out, "advisory:") || !strings.Contains(out, "332") || !strings.Contains(out, "local: no other record") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, out, errb := e.runAccept(t, s2, "--crossref", filepath.Join(e.dir, "l.json")); code != 0 || !strings.Contains(out, "judged local (check it)") {
		t.Fatalf("local verdicts are printed: %d\n%s\n%s", code, out, errb)
	}
}
