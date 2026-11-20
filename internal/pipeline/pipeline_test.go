package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/lang"
)

const spec = `# Tiny

## Core

- The program must print "hello" when run

## Extra

- The program must also define a function Twice that returns twice its argument
`

// scripted answers by the file named in the prompt; bad ones first if asked.
type scripted struct {
	bodies map[string][]string
	calls  map[string]int
}

func (s *scripted) Complete(_ context.Context, prompt string) (string, error) {
	for file, answers := range s.bodies {
		if strings.Contains(prompt, "Write the complete file `"+file+"`") {
			i := s.calls[file]
			s.calls[file]++
			if i >= len(answers) {
				i = len(answers) - 1
			}
			return "Here it is:\n```go\n" + answers[i] + "```\n", nil
		}
	}
	return "```go\npackage main\n```\n", nil
}

func setup(t *testing.T) (Config, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	repo, root := t.TempDir(), filepath.Join(t.TempDir(), "app")
	log, err := evidence.Open(filepath.Join(repo, ".graft", "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(repo, ".graft"), 0o755)
	return Config{
		Repo: repo, Root: root, Module: "tiny", Spec: spec, Conventions: "package main, standard library only", Tier: "low",
		Gates:  []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.TestRun, Timeout: time.Minute}},
		Policy: evidence.Policy{"low": {evidence.TestRun}},
		Log:    log,
	}, root
}

func TestAcceptIsIdempotentAndIdsAreStable(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	a1, err := Accept(repo, drafter.Drafts(spec), "test", now)
	if err != nil || len(a1) != 2 || !a1[0].Created {
		t.Fatalf("%v %+v", err, a1)
	}
	a2, err := Accept(repo, drafter.Drafts(spec), "test", now)
	if err != nil || len(a2) != 2 || a2[0].Created || a2[0].ID != a1[0].ID || a2[1].ID != a1[1].ID {
		t.Fatalf("re-accept changed identity: %v %+v", err, a2)
	}
	vs, _ := filepath.Glob(filepath.Join(repo, "provenance", ".versions", a1[0].ID, "*"))
	if len(vs) != 1 {
		t.Fatalf("unchanged re-accept minted versions: %v", vs)
	}
	if a1[0].Entry.File != "core.go" || a1[1].Entry.File != "extra.go" {
		t.Fatalf("files %s %s", a1[0].Entry.File, a1[1].Entry.File)
	}
}

func TestAcceptRefusesUndecided(t *testing.T) {
	bad := "# T\n\n## A\n\n- Users can add a book\n- Two books must never share an ISBN; a repeat is a conflict\n"
	if _, err := Accept(t.TempDir(), drafter.Drafts(bad), "test", time.Now()); err == nil {
		t.Fatal("accepted a draft with no confident proposal")
	}
}

func TestRunConvergesWithFeedbackAndIsUpToDateOnRerun(t *testing.T) {
	cfg, root := setup(t)
	acc, err := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m := &scripted{calls: map[string]int{}, bodies: map[string][]string{
		"core.go":  {"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"},
		"extra.go": {"package main\n\nfunc Twice(x int) int { return x * }\n", "package main\n\nfunc Twice(x int) int { return x * 2 }\n"},
	}}
	cfg.Model = m
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatalf("%v %+v", err, out)
	}
	if !out[0].Applied || out[0].Attempts != 1 || !out[1].Applied || out[1].Attempts != 2 {
		t.Fatalf("%+v", out)
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(b)) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	// Re-run: nothing changed, so no model calls (regions are re-verified only).
	calls := m.calls["core.go"] + m.calls["extra.go"]
	out2, err := Run(context.Background(), cfg, acc)
	if err != nil || out2[0].Attempts != 0 || out2[1].Attempts != 0 || m.calls["core.go"]+m.calls["extra.go"] != calls {
		t.Fatalf("re-run regenerated: %v %+v", err, out2)
	}
}

func TestRunStopsWhenARegionNeverConverges(t *testing.T) {
	cfg, _ := setup(t)
	acc, _ := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	cfg.Model = &scripted{calls: map[string]int{}, bodies: map[string][]string{"core.go": {"package main\n\nfunc main() { broken( }\n"}}}
	cfg.Attempts = 2
	out, err := Run(context.Background(), cfg, acc)
	if err == nil || len(out) != 1 || out[0].Applied || out[0].Attempts != 2 {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestPromptNeverShowsTheOracle(t *testing.T) {
	cfg, _ := setup(t)
	acc, _ := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	p, err := cfg.prompt(acc[0], "")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"oracle", "reference"} {
		if strings.Contains(strings.ToLower(p), banned) {
			t.Fatalf("prompt mentions %q", banned)
		}
	}
	if !strings.Contains(p, "The program must print \"hello\" when run") {
		t.Fatal("prompt lacks the section's statements")
	}
}

// Review on #12: "up to date" must not skip a changed gate. The existing body
// is re-verified under the current gates; if that fails, the region is
// regenerated, and a gate that rejects everything stops the run.
func TestUpToDateIsReverifiedUnderCurrentGates(t *testing.T) {
	cfg, _ := setup(t)
	acc, _ := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	m := &scripted{calls: map[string]int{}, bodies: map[string][]string{
		"core.go":  {"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"},
		"extra.go": {"package main\n\nfunc Twice(x int) int { return x * 2 }\n"},
	}}
	cfg.Model = m
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	before := len(cfg.Log.All())
	// Same gates: re-verified (evidence appended), no model calls.
	calls := m.calls["core.go"] + m.calls["extra.go"]
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	if m.calls["core.go"]+m.calls["extra.go"] != calls {
		t.Fatal("re-verification called the model")
	}
	if len(cfg.Log.All()) <= before {
		t.Fatal("up-to-date regions were not re-verified (no evidence appended)")
	}
	// A new gate that rejects everything: the old evidence must not stand in for it.
	cfg.Gates = append(cfg.Gates, converge.CommandGate{Label: "always fails", Argv: []string{"false"}, As: evidence.Eval})
	cfg.Policy = evidence.Policy{"low": {evidence.TestRun, evidence.Eval}}
	out, err := Run(context.Background(), cfg, acc)
	if err == nil || out[0].Applied {
		t.Fatalf("a failing new gate was skipped: %v %+v", err, out)
	}
	if m.calls["core.go"] == 1 {
		t.Fatal("a region failing re-verification was not regenerated")
	}
}

func TestHandEditRefusedBeforeAnyModelCall(t *testing.T) {
	cfg, root := setup(t)
	acc, _ := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	m := &scripted{calls: map[string]int{}, bodies: map[string][]string{
		"core.go":  {"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"},
		"extra.go": {"package main\n\nfunc Twice(x int) int { return x * 2 }\n"},
	}}
	cfg.Model = m
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "core.go")
	b, _ := os.ReadFile(p)
	edited := strings.Replace(string(b), `"hello"`, `"hello, edited"`, 1)
	os.WriteFile(p, []byte(edited), 0o644)
	// Make the record need regeneration too, so only the pre-check can stop the call.
	calls := m.calls["core.go"]
	_, err := Run(context.Background(), cfg, acc)
	if !errors.Is(err, converge.ErrHandEdited) {
		t.Fatalf("err = %v", err)
	}
	if m.calls["core.go"] != calls {
		t.Fatal("a model call was made for a hand-edited region")
	}
	if got, _ := os.ReadFile(p); string(got) != edited {
		t.Fatal("hand edit not preserved")
	}
}

func TestVisibleChecksAreInThePrompt(t *testing.T) {
	cfg, _ := setup(t)
	cfg.Visible = map[string]string{"smoke/smoke.go": "package main // SMOKE-MARKER\n"}
	acc, _ := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	p, err := cfg.prompt(acc[0], "")
	if err != nil || !strings.Contains(p, "SMOKE-MARKER") || !strings.Contains(p, "### smoke/smoke.go") {
		t.Fatalf("%v\n%s", err, p)
	}
}

// pyModel answers in a python fence and records the prompts it saw.
type pyModel struct{ prompts []string }

func (m *pyModel) Complete(_ context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	if strings.Contains(prompt, "Write the complete file `core.py`") {
		return "```python\nprint(\"hello\")\n```\n", nil
	}
	return "```python\ndef twice(x):\n    return x * 2\n```\n", nil
}

func TestPythonProfile(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("needs python3")
	}
	cfg, root := setup(t)
	cfg.Lang = lang.Profile{Name: "python", Ext: ".py", Fence: "python", Comment: "#",
		Preamble: "a complete module", Bootstrap: map[string]string{"main.py": "# {module} entry\n"},
		Run: []string{"python3", "main.py"}}
	cfg.Gates = []converge.Gate{converge.CommandGate{Label: "py_compile",
		Argv: []string{py, "-c", "import glob,py_compile;[py_compile.compile(f,doraise=True) for f in glob.glob('*.py')]"},
		As:   evidence.TestRun, Timeout: time.Minute}}
	m := &pyModel{}
	cfg.Model = m
	acc, err := AcceptExt(cfg.Repo, drafter.Drafts(spec), "test", time.Now(), ".py")
	if err != nil || acc[0].Entry.File != "core.py" || acc[1].Entry.File != "extra.py" {
		t.Fatalf("%v %+v", err, acc)
	}
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	boot, _ := os.ReadFile(filepath.Join(root, "main.py"))
	if string(boot) != "# tiny entry\n" {
		t.Fatalf("bootstrap: %q", boot)
	}
	core, _ := os.ReadFile(filepath.Join(root, "core.py"))
	if !strings.HasPrefix(string(core), "# graft:synth:begin ") || !strings.Contains(string(core), "print(\"hello\")") {
		t.Fatalf("markers must use the profile's comment:\n%s", core)
	}
	if out, err := exec.Command(py, filepath.Join(root, "core.py")).CombinedOutput(); err != nil || string(out) != "hello\n" {
		t.Fatalf("the generated file must run: %v %q", err, out)
	}
	last := m.prompts[len(m.prompts)-1]
	for _, want := range []string{"one file of a python program", "```python\n# graft:synth:begin", "in a single ```python block, a complete module"} {
		if !strings.Contains(last, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "package clause") {
		t.Fatal("a python prompt must not ask for a package clause")
	}
}

func TestSectionFilesPlaceRegions(t *testing.T) {
	cfg, _ := setup(t)
	opts := AcceptOptions{Files: map[string]string{"Extra": "extra/twice.go"}}
	acc, err := AcceptWith(cfg.Repo, drafter.Drafts(spec), "test", time.Now(), opts)
	if err != nil || acc[0].Entry.File != "core.go" || acc[1].Entry.File != "extra/twice.go" {
		t.Fatalf("%v %+v", err, acc)
	}
	// Sticky: dropping the key keeps the file; naming another file is an error.
	if acc2, err := AcceptWith(cfg.Repo, drafter.Drafts(spec), "test", time.Now(), AcceptOptions{}); err != nil || acc2[1].Entry.File != "extra/twice.go" {
		t.Fatalf("%v %+v", err, acc2)
	}
	if _, err := AcceptWith(cfg.Repo, drafter.Drafts(spec), "test", time.Now(), AcceptOptions{Files: map[string]string{"Extra": "other.go"}}); !errors.Is(err, ErrBadFile) {
		t.Fatalf("moving an accepted section must be refused: %v", err)
	}
}

func TestSectionFilesRefusals(t *testing.T) {
	twoExtras := "# T\n\n## A\n\n### Extra\n\n- It must do a\n\n## B\n\n### Extra\n\n- It must do b\n"
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "real"), 0o755)
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link"))
	cases := []struct {
		spec  string
		files map[string]string
		want  string
	}{
		{spec, map[string]string{"Extra": "core.go"}, "already"},                    // Core's default name
		{spec, map[string]string{"Core": "same.go", "Extra": "same.go"}, "already"}, // two configured
		{spec, map[string]string{"Extra": "main.go"}, "already exists"},             // a hand-written file
		{spec, map[string]string{"Extra": "link/e.go"}, "symlink"},
		{spec, map[string]string{"Extra": ".graft/x.go"}, "hidden"},
		{spec, map[string]string{"Extra": ".go"}, "clean relative"},
		{spec, map[string]string{"Extra": "../x.go"}, "clean relative"},
		{spec, map[string]string{"Extra": "/abs.go"}, "clean relative"},
		{spec, map[string]string{"Extra": "x.py"}, "clean relative"},
		{spec, map[string]string{"Extra": "a//b.go"}, "clean relative"},
		{spec, map[string]string{"Nope": "n.go"}, "names no section"},
		{twoExtras, map[string]string{"Extra": "x/e.go"}, "several sections"},
	}
	for _, c := range cases {
		_, err := AcceptWith(t.TempDir(), drafter.Drafts(c.spec), "test", time.Now(), AcceptOptions{Files: c.files, Root: root})
		if !errors.Is(err, ErrBadFile) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (want %q)", c.files, err, c.want)
		}
	}
	// Default names are held to the same rule: a hand-written core.go is not taken over.
	os.WriteFile(filepath.Join(root, "core.go"), []byte("package main\n"), 0o644)
	if _, err := AcceptWith(t.TempDir(), drafter.Drafts(spec), "test", time.Now(), AcceptOptions{Root: root}); !errors.Is(err, ErrBadFile) {
		t.Errorf("default name over a hand-written file: %v", err)
	}
}

// A section placed in a subdirectory package converges there, end to end.
func TestSectionInSubpackageConverges(t *testing.T) {
	cfg, root := setup(t)
	cfg.Spec = spec
	cfg.Model = &scripted{bodies: map[string][]string{
		"core.go":        {"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"},
		"extra/twice.go": {"package extra\n\n// Twice returns twice its argument.\nfunc Twice(x int) int { return x * 2 }\n"},
	}, calls: map[string]int{}}
	acc, err := AcceptWith(cfg.Repo, drafter.Drafts(spec), "test", time.Now(), AcceptOptions{Files: map[string]string{"Extra": "extra/twice.go"}, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "extra", "twice.go"))
	if err != nil || !strings.Contains(string(b), "package extra") || !strings.Contains(string(b), "graft:synth:begin") {
		t.Fatalf("%v\n%s", err, b)
	}
}
