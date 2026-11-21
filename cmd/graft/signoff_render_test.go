package main

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
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/render"
)

const flExample = "../../examples/freeq-lite/"

// echoModel answers a region prompt with the region file as it stands (the
// placeholder replaced), like the pipeline tests' model. It counts its calls.
type echoModel struct {
	root  string
	calls int
}

var askedFile = regexp.MustCompile("complete contents of `([^`]+)`")

func (m *echoModel) Complete(_ context.Context, prompt string) (string, error) {
	m.calls++
	g := askedFile.FindStringSubmatch(prompt)
	if g == nil {
		return "", errors.New("prompt does not ask for a region file")
	}
	b, err := os.ReadFile(filepath.Join(m.root, g[1]))
	if err != nil {
		return "", err
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.Contains(l, "graft:synth") && !strings.Contains(l, "sedum:") {
			keep = append(keep, l)
		}
	}
	code := strings.ReplaceAll(strings.Join(keep, "\n"), recording.Placeholder, "echo body")
	return "```go\n" + code + "\n// converged\n```\n", nil
}

// renderTree is a real graft run --render tree: freeq-lite, rendered by sedum,
// converged by pipeline.Run with an echo model.
type renderTree struct {
	t        *testing.T
	dir      string // graft.json, skeleton.json, spec.md
	repo     string
	root     string
	cfgPath  string
	specPath string
	skel     recording.Skeleton
	bin, gen string
}

func newRenderTree(t *testing.T) *renderTree {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	bin, err := exec.LookPath("sedum")
	gens := os.Getenv("SEDUM_GENERATORS")
	if err != nil || gens == "" {
		t.Skip("needs sedum on PATH and SEDUM_GENERATORS")
	}
	r := &renderTree{t: t, dir: t.TempDir(), repo: t.TempDir(), root: filepath.Join(t.TempDir(), "app"), bin: bin, gen: gens}
	copyFile(t, flExample+"skeleton.json", filepath.Join(r.dir, "skeleton.json"))
	copyFile(t, flExample+"conventions.md", filepath.Join(r.dir, "conventions.md"))
	r.specPath = filepath.Join(r.dir, "spec.md")
	copyFile(t, flExample+"spec/freeq-lite.md", r.specPath)
	r.cfgPath = filepath.Join(r.dir, "graft.json")
	os.WriteFile(r.cfgPath, []byte(`{"policy":{"low":["static_analysis","human_signoff"]},"skeleton":"skeleton.json",`+
		`"gates":[{"label":"go build","argv":["go","build","./..."],"kind":"static_analysis","timeout_sec":120}]}`), 0o644)
	r.loadSkeleton()
	return r
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *renderTree) loadSkeleton() {
	b, err := os.ReadFile(filepath.Join(r.dir, "skeleton.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	if r.skel, err = recording.LoadSkeleton(b); err != nil {
		r.t.Fatal(err)
	}
}

// run is graft run --accept --render with the given policy; it returns the outcomes.
func (r *renderTree) run(policy evidence.Policy) ([]pipeline.Outcome, error) {
	r.t.Helper()
	spec, _ := os.ReadFile(r.specPath)
	conv, _ := os.ReadFile(filepath.Join(r.dir, "conventions.md"))
	acc, err := pipeline.Accept(r.repo, drafter.Drafts(string(spec)), "test", time.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	log, err := evidence.Open(evidenceLogPath(r.repo, ""))
	if err != nil {
		r.t.Fatal(err)
	}
	skel := r.skel
	return pipeline.Run(context.Background(), pipeline.Config{
		Repo: r.repo, Root: r.root, Module: "freeqlite", Spec: string(spec), Conventions: string(conv), Tier: "low",
		Model: &echoModel{root: r.root}, Log: log, Policy: policy,
		Gates: []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."},
			As: evidence.StaticAnalysis, Timeout: 2 * time.Minute}},
		Skeleton: &skel, Sedum: render.Exec(r.bin), Generators: r.gen,
	}, acc)
}

// signAll signs off every region whose record the spec still covers, through
// the real CLI with its defaults (the log graft run reads) and --spec.
func (r *renderTree) signAll() int {
	r.t.Helper()
	spec, _ := os.ReadFile(r.specPath)
	ids, err := pipeline.SpecRecordIDs(r.repo, drafter.Drafts(string(spec)))
	if err != nil {
		r.t.Fatal(err)
	}
	regs, err := converge.ReadRegions(r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, g := range regs {
		covered := false
		for _, id := range ids {
			covered = covered || id == g.Owner.Record
		}
		if !covered {
			continue
		}
		code, out, errs := runWith("", []string{"signoff", "--config", r.cfgPath, "--records", r.repo, "--root", r.root,
			"--record", g.Owner.Record, "--path", g.Path, "--by", "alice", "--spec", r.specPath, "--yes"})
		if code != 0 {
			r.t.Fatalf("signoff %s: exit %d\n%s%s", g.Path, code, out, errs)
		}
		n++
	}
	return n
}

func calls(out []pipeline.Outcome) int {
	n := 0
	for _, o := range out {
		n += o.Calls
	}
	return n
}

var needsSignoff = evidence.Policy{"low": {evidence.StaticAnalysis, evidence.HumanSignoff}}

// A sign-off written by graft signoff is judged fresh by graft run --render
// itself: after signing every region, a run applies everything, with no model call.
func TestSignoffRenderTreeIsFreshToRun(t *testing.T) {
	r := newRenderTree(t)
	if out, err := r.run(evidence.Policy{"low": {evidence.StaticAnalysis}}); err != nil {
		t.Fatalf("first run: %v %+v", err, out)
	}
	out, err := r.run(needsSignoff)
	if err == nil || len(out) == 0 || len(out[len(out)-1].NeedsEvidence) == 0 {
		t.Fatalf("before signoff, run should need human_signoff: %v %+v", err, out)
	}
	if n := r.signAll(); n < 2 {
		t.Fatalf("signed %d regions", n)
	}
	out, err = r.run(needsSignoff)
	if err != nil {
		t.Fatalf("after signoff: %v\n%+v", err, out)
	}
	for _, o := range out {
		if !o.Applied {
			t.Fatalf("not applied: %+v", o)
		}
	}
	if calls(out) != 0 {
		t.Fatalf("run made %d model calls after the sign-offs", calls(out))
	}
}

// The removed-section case: the records index still holds the removed
// section's record, but run (and so signoff) hash the skeleton over the
// records the CURRENT spec resolves to.
func TestSignoffRenderTreeAfterASectionIsRemoved(t *testing.T) {
	r := newRenderTree(t)
	if out, err := r.run(evidence.Policy{"low": {evidence.StaticAnalysis}}); err != nil {
		t.Fatalf("first run: %v %+v", err, out)
	}
	spec, _ := os.ReadFile(r.specPath)
	from, to := strings.Index(string(spec), "## Leaving"), strings.Index(string(spec), "## Everything else")
	if from < 0 || to < from {
		t.Fatal("freeq-lite spec changed shape")
	}
	os.WriteFile(r.specPath, []byte(string(spec)[:from]+string(spec)[to:]), 0o644)
	var kept []recording.File
	for _, f := range r.skel.Files {
		if !strings.HasSuffix(f.Section, "Leaving") {
			kept = append(kept, f)
		}
	}
	if len(kept) != len(r.skel.Files)-1 {
		t.Fatalf("expected to drop exactly the Leaving file, kept %d of %d", len(kept), len(r.skel.Files))
	}
	r.skel.Files = kept
	// run --render again under the plain tier: the skeleton moved, everything re-verifies.
	if out, err := r.run(evidence.Policy{"low": {evidence.StaticAnalysis}}); err != nil {
		t.Fatalf("run after removal: %v %+v", err, out)
	}
	// The index still knows the removed record.
	idx, _ := pipeline.LoadIndex(r.repo)
	spec2, _ := os.ReadFile(r.specPath)
	ids, err := pipeline.SpecRecordIDs(r.repo, drafter.Drafts(string(spec2)))
	if err != nil || len(idx.Records) != len(ids)+1 {
		t.Fatalf("expected the index to hold one more record than the spec: %d vs %d (%v)", len(idx.Records), len(ids), err)
	}
	if out, err := r.run(needsSignoff); err == nil {
		t.Fatalf("should need human_signoff: %+v", out)
	}
	if n := r.signAll(); n != len(ids) {
		t.Fatalf("signed %d regions, want %d", n, len(ids))
	}
	out, err := r.run(needsSignoff)
	if err != nil {
		t.Fatalf("after signoff: %v\n%+v", err, out)
	}
	if calls(out) != 0 {
		t.Fatalf("run made %d model calls: the sign-offs were stale", calls(out))
	}
}
