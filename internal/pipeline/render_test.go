package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/render"
)

const flDir = "../../examples/freeq-lite/"

// echoModel answers every region prompt with the region file as it stands in
// the tree (the seed on a first run), and records the prompts it was given.
type echoModel struct {
	root    string
	prompts []string
	paths   []string
}

var askedPath = regexp.MustCompile("complete contents of `([^`]+)`")

func (m *echoModel) Complete(_ context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	g := askedPath.FindStringSubmatch(prompt)
	if g == nil {
		return "", errors.New("prompt does not ask for a region file")
	}
	m.paths = append(m.paths, g[1])
	b, err := os.ReadFile(filepath.Join(m.root, g[1]))
	if err != nil {
		return "", err
	}
	// Strip the region markers (the model must not write them) and add a line
	// so the answer differs from the placeholder.
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.Contains(l, "graft:synth") && !strings.Contains(l, "sedum:") {
			keep = append(keep, l)
		}
	}
	// The placeholder itself is refused; a different panic stands in for a real body.
	code := strings.ReplaceAll(strings.Join(keep, "\n"), recording.Placeholder, "echo body")
	return "```go\n" + code + "\n// converged\n```\n", nil
}

func renderSetup(t *testing.T, skeleton, specFile string) (Config, *echoModel, string) {
	t.Helper()
	return renderSetupIn(t, flDir, "freeqlite", skeleton, specFile)
}

func renderSetupIn(t *testing.T, dir, module, skeleton, specFile string) (Config, *echoModel, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	bin, err := exec.LookPath("sedum")
	gens := os.Getenv("SEDUM_GENERATORS")
	if err != nil || gens == "" {
		t.Skip("needs sedum on PATH and SEDUM_GENERATORS")
	}
	sk, err := os.ReadFile(dir + skeleton)
	if err != nil {
		t.Fatal(err)
	}
	skel, err := recording.LoadSkeleton(sk)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := os.ReadFile(dir + specFile)
	conv, _ := os.ReadFile(dir + "conventions.md")
	repo, root := t.TempDir(), filepath.Join(t.TempDir(), "app")
	os.MkdirAll(filepath.Join(repo, ".graft"), 0o755)
	log, err := evidence.Open(filepath.Join(repo, ".graft", "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m := &echoModel{root: root}
	return Config{
		Repo: repo, Root: root, Module: module, Spec: string(sp), Conventions: string(conv), Tier: "low",
		Model: m, Log: log, Policy: evidence.Policy{"low": {evidence.StaticAnalysis}},
		Gates: []converge.Gate{
			converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.StaticAnalysis, Timeout: 2 * time.Minute},
		},
		Skeleton: &skel, Sedum: render.Exec(bin), Generators: gens,
	}, m, string(sp)
}

func accept(t *testing.T, cfg Config, spec string) []Accepted {
	t.Helper()
	acc, err := Accept(cfg.Repo, drafter.Drafts(spec), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			b, _ := os.ReadFile(p)
			r, _ := filepath.Rel(root, p)
			if r == filepath.Join(".sedum", "run.log") {
				return nil // sedum's append-only run log, not part of the tree's content
			}
			out[r] = string(b)
		}
		return nil
	})
	return out
}

func growCheck(t *testing.T, cfg Config) {
	t.Helper()
	cleanup, err := cfg.sedumEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cfg.Sedum.GrowCheck(context.Background(), cfg.Root); err != nil {
		t.Fatalf("grow --check: %v", err)
	}
}

func TestRunRenderFakeModel(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		if !o.Applied {
			t.Fatalf("not applied: %+v", o)
		}
	}
	if len(m.prompts) == 0 {
		t.Fatal("no model calls")
	}
	for i, p := range m.prompts {
		if !recording.IsSynthPath(m.paths[i], "_synth") {
			t.Fatalf("model asked for %s, not a synth file", m.paths[i])
		}
		if !strings.Contains(p, "Declares: the helpers you must implement") || !strings.Contains(p, "Body(") {
			t.Fatalf("prompt for %s lacks the Declares signatures", m.paths[i])
		}
		if strings.Contains(p, "Write the complete file") {
			t.Fatalf("prompt for %s asks for a whole file", m.paths[i])
		}
	}
	files := snapshot(t, cfg.Root)
	for _, f := range []string{"running.go", "running_synth.go", "go.mod"} {
		if _, ok := files[f]; !ok {
			t.Fatalf("missing %s", f)
		}
	}
	for _, n := range []string{"generators", "provenance", "generators.partial"} {
		if _, err := os.Lstat(filepath.Join(cfg.Root, n)); err == nil {
			t.Fatalf("%s left in the tree", n)
		}
	}
	if b, err := exec.Command("go", "build", "-C", cfg.Root, "./...").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	growCheck(t, cfg)
}

func TestRunRenderRerunNoop(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	before, calls := snapshot(t, cfg.Root), len(m.prompts)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	if len(m.prompts) != calls {
		t.Fatalf("second run made %d model calls", len(m.prompts)-calls)
	}
	after := snapshot(t, cfg.Root)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	if len(after) != len(before) {
		t.Error("file set changed")
	}
	growCheck(t, cfg)
}

func TestRunRenderEvolve(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	if _, err := Run(context.Background(), cfg, accept(t, cfg, spec)); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, cfg.Root)
	// v2: the same repo and root, the Topics section and its skeleton file added.
	cfg2, _, spec2 := renderSetup(t, "skeleton-v2.json", "spec-v2/freeq-lite.md")
	cfg2.Repo, cfg2.Root, cfg2.Log, cfg2.Spec = cfg.Repo, cfg.Root, cfg.Log, spec2
	m.prompts, m.paths = nil, nil
	cfg2.Model = m
	if _, err := Run(context.Background(), cfg2, accept(t, cfg2, spec2)); err != nil {
		t.Fatal(err)
	}
	if len(m.paths) != 1 || m.paths[0] != "topics_synth.go" {
		t.Fatalf("regenerated %v, want only topics_synth.go", m.paths)
	}
	after := snapshot(t, cfg.Root)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	for _, f := range []string{"topics.go", "topics_synth.go"} {
		if _, ok := after[f]; !ok {
			t.Errorf("missing %s", f)
		}
	}
	growCheck(t, cfg2)
}

func TestRunRenderRefusesHandEditOfSedumFile(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfg.Root, "running.go")
	b, _ := os.ReadFile(p)
	edited := strings.Replace(string(b), "freeq-lite", "freeq-edited", 1)
	if edited == string(b) {
		t.Fatal("test edit did not change the file")
	}
	os.WriteFile(p, []byte(edited), 0o644)
	calls := len(m.prompts)
	_, err := Run(context.Background(), cfg, acc)
	if !errors.Is(err, render.ErrHandEdited) {
		t.Fatalf("err = %v, want render.ErrHandEdited", err)
	}
	if !strings.Contains(err.Error(), "before any model call") {
		t.Fatalf("unclear error: %v", err)
	}
	if len(m.prompts) != calls {
		t.Fatal("model called")
	}
	if got, _ := os.ReadFile(p); string(got) != edited {
		t.Fatal("hand edit not preserved")
	}
}

// failModel is a model command that always fails (infrastructure error).
type failModel struct{ calls int }

func (m *failModel) Complete(context.Context, string) (string, error) {
	m.calls++
	return "", errors.New("down")
}

// placeholderModel answers with the seeded placeholder, as if it did nothing.
type placeholderModel struct{ calls int }

func (m *placeholderModel) Complete(_ context.Context, p string) (string, error) {
	m.calls++
	return "```go\npackage main\n\nfunc x() { panic(\"" + recording.Placeholder + "\") }\n```\n", nil
}

func synthFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range snapshot(t, root) {
		if recording.IsSynthPath(k, "_synth") {
			out[k] = v
		}
	}
	return out
}

// Review on #19, finding 1: seeds left by a failed run must stay pending when
// the tier changes (the seed marker differs, the body does not).
func TestRunRenderTierChangeKeepsSeedsPending(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	down := &failModel{}
	cfg.Model = down
	if _, err := Run(context.Background(), cfg, acc); !errors.Is(err, ErrModel) {
		t.Fatalf("run 1: %v, want ErrModel", err)
	}
	seeds := synthFiles(t, cfg.Root)
	if len(seeds) == 0 {
		t.Fatal("no seeds left after the failed run")
	}
	cfg2 := cfg
	cfg2.Tier = "medium"
	cfg2.Policy = evidence.Policy{"low": {evidence.StaticAnalysis}, "medium": {evidence.StaticAnalysis}}
	// Still down: the seeds must not be accepted as converged.
	down2 := &failModel{}
	cfg2.Model = down2
	if _, err := Run(context.Background(), cfg2, acc); !errors.Is(err, ErrModel) {
		t.Fatalf("run 2 with the model down: %v, want ErrModel (seed accepted without a call?)", err)
	}
	if down2.calls == 0 {
		t.Fatal("run 2 made no model call")
	}
	// Model back: every region is really converged.
	m := &echoModel{root: cfg.Root}
	cfg2.Model = m
	out, err := Run(context.Background(), cfg2, acc)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		if !o.Applied {
			t.Fatalf("not applied: %+v", o)
		}
	}
	if len(m.prompts) == 0 {
		t.Fatal("no model calls after the tier change")
	}
	for k, v := range synthFiles(t, cfg.Root) {
		if strings.Contains(v, recording.Placeholder) {
			t.Fatalf("%s still holds the placeholder", k)
		}
	}
}

// A model that answers with the placeholder never converges; nothing is written.
func TestRunRenderRefusesPlaceholderAnswer(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	pm := &placeholderModel{}
	cfg.Model = pm
	out, err := Run(context.Background(), cfg, acc)
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("err = %v, want did-not-converge", err)
	}
	if pm.calls != 3 {
		t.Fatalf("calls = %d, want 3 attempts", pm.calls)
	}
	if len(out) == 0 || out[len(out)-1].Applied {
		t.Fatalf("outcome %+v", out)
	}
	if n := len(evidenceLines(t, cfg)); n != 0 {
		t.Fatalf("%d evidence entries written for a placeholder", n)
	}
}

func evidenceLines(t *testing.T, cfg Config) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.Repo, ".graft", "evidence.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// Review on #19, finding 5: a failed generators copy leaves nothing behind.
func TestSedumEnvFailedCopyLeavesNoPartial(t *testing.T) {
	cfg := Config{Root: t.TempDir(), Repo: t.TempDir(), Generators: filepath.Join(t.TempDir(), "missing")}
	if _, err := cfg.sedumEnv(); err == nil {
		t.Fatal("want an error for a missing generators dir")
	}
	for _, n := range []string{"generators", "generators.partial", "provenance"} {
		if _, err := os.Lstat(filepath.Join(cfg.Root, n)); err == nil {
			t.Fatalf("%s left behind", n)
		}
	}
	// A stale partial from a killed run does not block the next one.
	gens := t.TempDir()
	os.WriteFile(filepath.Join(gens, "x"), []byte("x"), 0o644)
	cfg.Generators = gens
	os.MkdirAll(filepath.Join(cfg.Root, "generators.partial"), 0o755)
	cleanup, err := cfg.sedumEnv()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}
