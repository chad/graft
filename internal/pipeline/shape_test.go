package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
)

// editSkeleton gives cfg an edited copy of its skeleton.
func editSkeleton(t *testing.T, cfg *Config, edit func(*recording.Skeleton)) {
	t.Helper()
	b, err := json.Marshal(cfg.Skeleton)
	if err != nil {
		t.Fatal(err)
	}
	var sk recording.Skeleton
	if err := json.Unmarshal(b, &sk); err != nil {
		t.Fatal(err)
	}
	edit(&sk)
	cfg.Skeleton = &sk
}

func fileOfSkeleton(t *testing.T, sk *recording.Skeleton, path string) *recording.File {
	t.Helper()
	for i := range sk.Files {
		if sk.Files[i].Path == path {
			return &sk.Files[i]
		}
	}
	t.Fatalf("no %s in the skeleton", path)
	return nil
}

func byFile(out []Outcome) map[string]Outcome {
	m := map[string]Outcome{}
	for _, o := range out {
		m[o.File] = o
	}
	return m
}

func versions(t *testing.T, repo string, acc []Accepted) map[string]int {
	t.Helper()
	s, err := records.OpenDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int{}
	for _, a := range acc {
		p, err := s.Current(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		m[a.ID] = p.Version
	}
	return m
}

func sortedPaths(p []string) []string {
	p = append([]string(nil), p...)
	sort.Strings(p)
	return p
}

func TestRenderImportWritesShapesAndReimportIsANoop(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	s, _ := records.OpenDir(cfg.Repo)
	shaped := 0
	for _, a := range acc {
		p, _ := s.Current(a.ID)
		if len(p.Shape) > 0 {
			shaped++
		}
		if p.GenHash == p.SedumGen && len(p.Shape) > 0 {
			t.Errorf("%s: the shape did not reach gen_hash", a.ID)
		}
	}
	if shaped != len(cfg.Skeleton.Files) {
		t.Fatalf("%d records carry a shape, want one per skeleton file (%d)", shaped, len(cfg.Skeleton.Files))
	}
	v1, calls := versions(t, cfg.Repo, acc), len(m.prompts)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	if v2 := versions(t, cfg.Repo, acc); !equalInts(v1, v2) {
		t.Errorf("re-importing the same skeleton changed versions: %v -> %v", v1, v2)
	}
	if len(m.prompts) != calls {
		t.Errorf("re-import made %d model calls", len(m.prompts)-calls)
	}
}

func equalInts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// DoD 1: a doc-contract-only edit to a stub makes exactly that region stale
// and regenerates it; every other region is re-verified with no model call.
func TestRenderDocContractEditRegeneratesOnlyThatRegion(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	m.prompts, m.paths = nil, nil
	editSkeleton(t, &cfg, func(sk *recording.Skeleton) {
		f := fileOfSkeleton(t, sk, "keepalive.go")
		f.Stubs[0].Doc += " Answer PING with PONG and the same token."
	})
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.paths; len(got) != 1 || got[0] != "keepalive_synth.go" {
		t.Fatalf("model asked for %v, want only keepalive_synth.go", got)
	}
	for f, o := range byFile(out) {
		switch f {
		case "keepalive_synth.go":
			if o.Reason != "gen_hash" || o.Calls == 0 {
				t.Errorf("keepalive: %+v", o)
			}
		default:
			if o.Reason != "skeleton_hash" || o.Calls != 0 || o.Regenerated {
				t.Errorf("%s: %+v, want skeleton_hash re-verified with no model call", f, o)
			}
		}
	}
	growCheck(t, cfg)
}

// DoD 2: a Client field edit moves Running's gen_hash (it owns the file), so
// Running regenerates; every other region is skeleton_hash-stale and is
// re-verified under the gates with no model call.
func TestRenderClientFieldEdit(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	m.prompts, m.paths = nil, nil
	editSkeleton(t, &cfg, func(sk *recording.Skeleton) {
		f := fileOfSkeleton(t, sk, "running.go")
		for i := range f.Decls {
			if f.Decls[i].Name == "Client" {
				f.Decls[i].Code = strings.Replace(f.Decls[i].Code, "\tout        chan string\n", "\tout        chan string\n\tAway       string\n", 1)
			}
		}
	})
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.paths; len(got) != 1 || got[0] != "running_synth.go" {
		t.Fatalf("model asked for %v, want only running_synth.go", got)
	}
	n := 0
	for f, o := range byFile(out) {
		if f == "running_synth.go" {
			if o.Reason != "gen_hash" || o.Calls == 0 {
				t.Errorf("running: %+v", o)
			}
			continue
		}
		n++
		if o.Reason != "skeleton_hash" || o.Calls != 0 || o.Regenerated {
			t.Errorf("%s: %+v", f, o)
		}
	}
	if n == 0 {
		t.Fatal("no other regions")
	}
	if b, _ := os.ReadFile(filepath.Join(cfg.Root, "running.go")); !strings.Contains(string(b), "Away") {
		t.Error("running.go does not carry the new field")
	}
	growCheck(t, cfg)
}

// failNth is a gate that fails its nth run and passes every other.
type failNth struct {
	n    int
	runs *int
}

func (g failNth) Name() string        { return "fail-nth" }
func (g failNth) Kind() evidence.Kind { return evidence.StaticAnalysis }
func (g failNth) Run(context.Context, string) (evidence.Verdict, string, error) {
	*g.runs++
	if *g.runs == g.n {
		return evidence.Fail, "forced failure", nil
	}
	return evidence.Pass, "ok", nil
}

// Only a region whose old body fails the re-check is regenerated: here the
// second region checked (Registration; Running, the first, is regenerated
// because the edited Client is its own shape) fails once under the gates.
func TestRenderSkeletonChangeRegeneratesOnlyTheBodyThatFails(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	m.prompts, m.paths = nil, nil
	runs := 0
	cfg.Gates = append(cfg.Gates, failNth{n: 2, runs: &runs})
	editSkeleton(t, &cfg, func(sk *recording.Skeleton) {
		f := fileOfSkeleton(t, sk, "running.go")
		for i := range f.Decls {
			if f.Decls[i].Name == "Client" {
				f.Decls[i].Code = strings.Replace(f.Decls[i].Code, "\tout        chan string\n", "\tout        chan string\n\tAway       string\n", 1)
			}
		}
	})
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"registration_synth.go", "running_synth.go"}
	if got := sortedPaths(m.paths); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("model asked for %v, want %v", got, want)
	}
	o := byFile(out)["registration_synth.go"]
	if o.Reason != "skeleton_hash" || !o.Regenerated || o.Calls == 0 {
		t.Errorf("registration: %+v, want a skeleton_hash re-check that failed and regenerated", o)
	}
}

// Condition C: evidence from before M11a has no skeleton hash. In render mode
// it is skeleton_hash-stale: re-verified with no model call, and fresh
// evidence then pins the skeleton.
func TestRenderPreM11aEvidenceIsReverifiedWithoutAModelCall(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	// Rewrite the log as an older graft would have: same entries, no skeleton hash.
	old, err := evidence.Open(filepath.Join(t.TempDir(), "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range cfg.Log.All() {
		e.Pin.SkeletonHash, e.ID, e.Prev = "", "", ""
		if _, err := old.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Log = old
	before := len(old.All())
	m.prompts, m.paths = nil, nil
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.prompts) != 0 {
		t.Fatalf("pre-M11a evidence cost %d model calls", len(m.prompts))
	}
	for f, o := range byFile(out) {
		if strings.HasSuffix(f, "_synth.go") && (o.Reason != "skeleton_hash" || o.Regenerated) {
			t.Errorf("%s: %+v", f, o)
		}
	}
	pinned := 0
	for _, e := range old.All()[before:] {
		if e.Pin.SkeletonHash != "" {
			pinned++
		}
	}
	if pinned == 0 {
		t.Error("no fresh evidence pins the skeleton")
	}
	// And a third run is quiet again.
	n := len(old.All())
	out, err = Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	for f, o := range byFile(out) {
		if o.Reason != "" {
			t.Errorf("third run, %s: %+v", f, o)
		}
	}
	_ = n
}
