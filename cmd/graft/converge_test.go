package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureRecords = "../../internal/records/testdata/base"
const fixtureID = "prov-2026-aaaa0001"

func convergeSetup(t *testing.T, gateArgv string) (root, cfg, logp string) {
	t.Helper()
	root = t.TempDir()
	os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o644)
	cfg = filepath.Join(t.TempDir(), "graft.json")
	c := `{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["` + gateArgv + `"],"kind":"test_run","timeout_sec":10}]}`
	os.WriteFile(cfg, []byte(c), 0o644)
	logp = filepath.Join(t.TempDir(), "ev", "evidence.jsonl")
	return
}

func convergeArgs(root, cfg, logp string, extra ...string) []string {
	return append([]string{"converge", "--config", cfg, "--records", fixtureRecords, "--log", logp, "--root", root,
		"--record", fixtureID, "--path", "app.go", "--tier", "low"}, extra...)
}

func runWith(stdin string, args []string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run2(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestConvergeCLIApplies(t *testing.T) {
	root, cfg, logp := convergeSetup(t, "true")
	code, out, errs := runWith("func F() {}\n", convergeArgs(root, cfg, logp, "-"))
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if !strings.Contains(out, "app.go applied") || !strings.Contains(out, "guarantee: checked") {
		t.Fatal(out)
	}
	b, _ := os.ReadFile(filepath.Join(root, "app.go"))
	if !strings.Contains(string(b), "func F() {}") {
		t.Fatalf("tree:\n%s", b)
	}
	if fi, err := os.Stat(logp); err != nil || fi.Size() == 0 {
		t.Fatal("no evidence written")
	}
}

func TestConvergeCLIFailingGateIsExit1AndUnchanged(t *testing.T) {
	root, cfg, logp := convergeSetup(t, "false")
	code, out, _ := runWith("func F() {}\n", convergeArgs(root, cfg, logp, "-"))
	if code != 1 || !strings.Contains(out, "NOT applied") || !strings.Contains(out, "failing: test_run") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app.go")); string(b) != "package app\n" {
		t.Fatal("tree changed")
	}
}

func TestConvergeCLIJSON(t *testing.T) {
	root, cfg, logp := convergeSetup(t, "true")
	body := filepath.Join(t.TempDir(), "body.go")
	os.WriteFile(body, []byte("func G() {}\n"), 0o644)
	code, out, errs := runWith("", convergeArgs(root, cfg, logp, "--json", body))
	if code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	var d struct {
		Applied   bool
		Guarantee string
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || !d.Applied || d.Guarantee != "checked" {
		t.Fatalf("%v %s", err, out)
	}
}

func TestConvergeCLIRefusals(t *testing.T) {
	root, cfg, logp := convergeSetup(t, "true")
	bad := filepath.Join(t.TempDir(), "bad.json")
	// Valid gates, so only the unknown field can cause the refusal.
	os.WriteFile(bad, []byte(`{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}],"surprise":1}`), 0o644)
	cases := map[string][]string{
		"unknown record":   {"converge", "--config", cfg, "--records", fixtureRecords, "--log", logp, "--root", root, "--record", "prov-nope", "--path", "app.go", "--tier", "low", "-"},
		"escaping path":    {"converge", "--config", cfg, "--records", fixtureRecords, "--log", logp, "--root", root, "--record", fixtureID, "--path", "../x.go", "--tier", "low", "-"},
		"unknown tier":     {"converge", "--config", cfg, "--records", fixtureRecords, "--log", logp, "--root", root, "--record", fixtureID, "--path", "app.go", "--tier", "zzz", "-"},
		"unknown config":   {"converge", "--config", bad, "--records", fixtureRecords, "--log", logp, "--root", root, "--record", fixtureID, "--path", "app.go", "--tier", "low", "-"},
		"missing --record": {"converge", "--config", cfg, "--path", "app.go", "--tier", "low", "-"},
	}
	for name, args := range cases {
		if code, _, _ := runWith("func F() {}\n", args); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app.go")); string(b) != "package app\n" {
		t.Fatal("tree changed")
	}
}

func TestConvergeCLIConfigIsStrict(t *testing.T) {
	root, _, logp := convergeSetup(t, "true")
	for name, cfgText := range map[string]string{
		"trailing object": `{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}{"policy":{}}`,
		"unknown kind":    `{"policy":{"low":["test_rnu"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}`,
		"gate kind":       `{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"vibes"}]}`,
		"empty argv0":     `{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":[""],"kind":"test_run"}]}`,
		"negative":        `{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run","timeout_sec":-1}]}`,
		"duplicate key":   `{"policy":{"low":["eval"]},"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}`,
		"duplicate tier":  `{"policy":{"low":["eval"],"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}`,
		"upper-case key":  `{"POLICY":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}`,
		"upper gate key":  `{"policy":{"low":["test_run"]},"gates":[{"LABEL":"g","argv":["true"],"kind":"test_run"}]}`,
		"null tier":       `{"policy":{"low":null},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}`,
		"null gates":      `{"policy":{"low":["test_run"]},"gates":null}`,
		"trailing junk":   `{"policy":{"low":["test_run"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]} xyz`,
		"signoff gate":    `{"policy":{"low":["human_signoff"]},"gates":[{"label":"me","argv":["true"],"kind":"human_signoff"}]}`,
		"empty policy":    `{"policy":{},"gates":[{"label":"g","argv":["true"],"kind":"test_run"}]}`,
	} {
		cfg := filepath.Join(t.TempDir(), "graft.json")
		os.WriteFile(cfg, []byte(cfgText), 0o644)
		if code, _, _ := runWith("func F() {}\n", convergeArgs(root, cfg, logp, "-")); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app.go")); string(b) != "package app\n" {
		t.Fatal("tree changed")
	}
}

// Review on #9: a later gate that cannot start, after an earlier one ran, is
// exit 3 (checking had begun), not 2 (refused before checking).
func TestConvergeCLIFailureAfterCheckingBeganIsExit3(t *testing.T) {
	root, _, logp := convergeSetup(t, "true")
	cfg := filepath.Join(t.TempDir(), "graft.json")
	os.WriteFile(cfg, []byte(`{"policy":{"low":["test_run"]},"gates":[{"label":"ok","argv":["true"],"kind":"test_run"},{"label":"missing","argv":["no-such-binary-graft"],"kind":"eval"}]}`), 0o644)
	code, _, errs := runWith("func F() {}\n", convergeArgs(root, cfg, logp, "-"))
	if code != 3 || !strings.Contains(errs, "after checking began") {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app.go")); string(b) != "package app\n" {
		t.Fatal("tree changed")
	}
}

// Review on #9: neither flags nor the environment can set the pin.
func TestConvergeCLIPinComesOnlyFromRecords(t *testing.T) {
	root, cfg, logp := convergeSetup(t, "true")
	for _, f := range []string{"--version=99", "--gen=forged", "--gen-hash=forged"} {
		args := append(convergeArgs(root, cfg, logp), f, "-")
		if code, _, _ := runWith("func F() {}\n", args); code != 2 {
			t.Errorf("%s accepted", f)
		}
	}
	for _, k := range []string{"GRAFT_VERSION", "GRAFT_GEN_HASH", "GRAFT_PIN"} {
		t.Setenv(k, "forged")
	}
	code, out, errs := runWith("func F() {}\n", convergeArgs(root, cfg, logp, "--json", "-"))
	if code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	var d struct {
		Evidence []struct {
			Pin struct {
				Version int
				GenHash string `json:"gen_hash"`
			} `json:"pin"`
		}
	}
	json.Unmarshal([]byte(out), &d)
	if len(d.Evidence) != 1 || d.Evidence[0].Pin.Version != 1 || !strings.HasPrefix(d.Evidence[0].Pin.GenHash, "sha256:") {
		t.Fatalf("pin not from the record: %s", out)
	}
}

func TestConvergeCLIRefusalCreatesNoLogDir(t *testing.T) {
	root, cfg, logp := convergeSetup(t, "true")
	runWith("x", []string{"converge", "--config", cfg, "--records", fixtureRecords, "--log", logp, "--root", root, "--record", "prov-nope", "--path", "app.go", "--tier", "low", "-"})
	if _, err := os.Stat(filepath.Dir(logp)); !os.IsNotExist(err) {
		t.Fatal("log dir created on a refusal")
	}
}
