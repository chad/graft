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
	return "```go\n" + strings.Join(keep, "\n") + "\n// converged\n```\n", nil
}

func renderSetup(t *testing.T, skeleton, specFile string) (Config, *echoModel, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	bin, err := exec.LookPath("sedum")
	gens := os.Getenv("SEDUM_GENERATORS")
	if err != nil || gens == "" {
		t.Skip("needs sedum on PATH and SEDUM_GENERATORS")
	}
	sk, err := os.ReadFile(flDir + skeleton)
	if err != nil {
		t.Fatal(err)
	}
	skel, err := recording.LoadSkeleton(sk)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := os.ReadFile(flDir + specFile)
	conv, _ := os.ReadFile(flDir + "conventions.md")
	repo, root := t.TempDir(), filepath.Join(t.TempDir(), "app")
	os.MkdirAll(filepath.Join(repo, ".graft"), 0o755)
	log, err := evidence.Open(filepath.Join(repo, ".graft", "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m := &echoModel{root: root}
	return Config{
		Repo: repo, Root: root, Module: "freeqlite", Spec: string(sp), Conventions: string(conv), Tier: "low",
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
		if !recording.IsSynthPath(m.paths[i]) {
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
	if _, ok := files["generators/go"]; ok {
		t.Fatal("generators left in the tree")
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
