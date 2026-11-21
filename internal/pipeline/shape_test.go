package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

var recordID = regexp.MustCompile(`prov-[0-9]{4}-[0-9a-f]{8}`)

// DoD 3: freeq-lite imported from today's skeleton.json renders a tree
// byte-identical to the one graft rendered before skeletons were record
// content. The golden file holds the digest of every sedum-rendered file,
// produced by the code before M11a (record ids are random and are masked).
func TestRenderImportedFreeqLiteTreeIsByteIdentical(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	if _, err := Run(context.Background(), cfg, accept(t, cfg, spec)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/freeqlite-tree.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for k, v := range snapshot(t, cfg.Root) {
		if strings.HasSuffix(k, "_synth.go") || strings.HasPrefix(k, ".sedum") {
			continue
		}
		s := sha256.Sum256([]byte(recordID.ReplaceAllString(v, "prov-ID")))
		got[k] = hex.EncodeToString(s[:])
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s differs from the pre-M11a render", k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s is new", k)
		}
	}
}

// DoD 5: the ledger-lite cross-section rules (Journal, Freezing) live in the
// shared contract. Changing one after the code exists makes exactly the
// region that owns the contract stale (its shape moved: regenerated) and
// leaves every other region re-verified with no model call.
func TestRenderLedgerLiteCrossSectionRuleEvolves(t *testing.T) {
	const dir = "../../examples/ledger-lite/"
	for _, tc := range []struct{ rule, stub, from, to string }{
		{"Freezing", "Debitable", `a frozen account gives 423 "account frozen"`, `a frozen account gives 423 "account is frozen"`},
		{"Journal", "Post", "appends one journal Entry (seq counting from 1", "appends one journal Entry (seq counting from 0"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			cfg, m, spec := renderSetupIn(t, dir, "ledgerlite", "skeleton.json", "spec/ledger-lite.md")
			acc := accept(t, cfg, spec)
			if _, err := Run(context.Background(), cfg, acc); err != nil {
				t.Fatal(err)
			}
			m.prompts, m.paths = nil, nil
			editSkeleton(t, &cfg, func(sk *recording.Skeleton) {
				f := fileOfSkeleton(t, sk, "running.go")
				for i := range f.Stubs {
					if f.Stubs[i].Name == tc.stub {
						if !strings.Contains(f.Stubs[i].Doc, tc.from) {
							t.Fatalf("the %s contract no longer says %q", tc.stub, tc.from)
						}
						f.Stubs[i].Doc = strings.Replace(f.Stubs[i].Doc, tc.from, tc.to, 1)
					}
				}
			})
			out, err := Run(context.Background(), cfg, acc)
			if err != nil {
				t.Fatal(err)
			}
			if len(m.paths) != 1 || m.paths[0] != "running_synth.go" {
				t.Fatalf("model asked for %v, want only running_synth.go", m.paths)
			}
			others := 0
			for f, o := range byFile(out) {
				if f == "running_synth.go" {
					if o.Reason != "gen_hash" {
						t.Errorf("running: %+v", o)
					}
					continue
				}
				others++
				if o.Reason != "skeleton_hash" || o.Calls != 0 || o.Regenerated {
					t.Errorf("%s: %+v", f, o)
				}
			}
			if others != 6 {
				t.Errorf("%d other regions, want 6", others)
			}
		})
	}
}

// A tree converged by graft before M11a (testdata/premain: made by the code at
// origin/main, 7 model calls) must re-verify on its first --render run with
// no model call: the import moves every gen_hash, but the markers carry the
// shape-less gen, so only the skeleton changed.
func TestRenderUpgradeFromPreM11aTreeCostsNoModelCalls(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	// The fixture's Go files are stored as *.go.fixture so gofmt and go vet leave
	// the bytes (which the region markers hash) alone.
	for d, dst := range map[string]string{"repo": cfg.Repo, "app": cfg.Root} {
		os.RemoveAll(dst)
		src := filepath.Join("testdata", "premain", d)
		err := filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			to := filepath.Join(dst, strings.TrimSuffix(rel, ".fixture"))
			if e.IsDir() {
				return os.MkdirAll(to, 0o755)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(to, b, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	log, err := evidence.Open(filepath.Join(cfg.Repo, ".graft", "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Log = log
	acc := accept(t, cfg, spec)
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.prompts) != 0 {
		t.Fatalf("upgrading a converged tree made %d model calls", len(m.prompts))
	}
	n := 0
	for f, o := range byFile(out) {
		if !strings.HasSuffix(f, "_synth.go") {
			continue
		}
		n++
		if o.Reason != "skeleton_hash" || o.Regenerated || o.Calls != 0 || !o.Applied {
			t.Errorf("%s: %+v", f, o)
		}
	}
	if n != 7 {
		t.Errorf("%d regions, want 7", n)
	}
	// The markers now carry the full gen, so the next run is quiet.
	out, err = Run(context.Background(), cfg, acc)
	if err != nil || len(m.prompts) != 0 {
		t.Fatalf("second run: %v, %d calls", err, len(m.prompts))
	}
	for f, o := range byFile(out) {
		if o.Reason != "" {
			t.Errorf("second run, %s: %+v", f, o)
		}
	}
	growCheck(t, cfg)
}

// M1: a body that passes every gate but lacks required non-executable
// evidence is not sent to the model; the run says what is missing.
func TestRenderMissingSignoffMakesNoModelCall(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	m.prompts, m.paths = nil, nil
	cfg.Policy = evidence.Policy{"low": {evidence.StaticAnalysis, evidence.HumanSignoff}}
	out, err := Run(context.Background(), cfg, acc)
	if err == nil || !strings.Contains(err.Error(), "needs [human_signoff] evidence") {
		t.Fatalf("err = %v", err)
	}
	if len(m.prompts) != 0 {
		t.Errorf("%d model calls for evidence a model cannot produce", len(m.prompts))
	}
	last := out[len(out)-1]
	if last.Regenerated || len(last.NeedsEvidence) != 1 || last.NeedsEvidence[0] != evidence.HumanSignoff {
		t.Errorf("%+v", last)
	}
}

// On the first run after an upgrade, an edit to a record's own shape made at
// the same time is regenerated, not just re-verified: the old recording in the
// tree holds the stub docs the body was written against.
func TestRenderUpgradeWithAShapeEditRegeneratesThatRegion(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	// The fixture's Go files are stored as *.go.fixture so gofmt and go vet leave
	// the bytes (which the region markers hash) alone.
	for d, dst := range map[string]string{"repo": cfg.Repo, "app": cfg.Root} {
		os.RemoveAll(dst)
		src := filepath.Join("testdata", "premain", d)
		err := filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			to := filepath.Join(dst, strings.TrimSuffix(rel, ".fixture"))
			if e.IsDir() {
				return os.MkdirAll(to, 0o755)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(to, b, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	log, err := evidence.Open(filepath.Join(cfg.Repo, ".graft", "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Log = log
	editSkeleton(t, &cfg, func(sk *recording.Skeleton) {
		f := fileOfSkeleton(t, sk, "keepalive.go")
		f.Stubs[0].Doc += " Also answer PONG with nothing."
	})
	acc := accept(t, cfg, spec)
	out, err := Run(context.Background(), cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.paths; len(got) != 1 || got[0] != "keepalive_synth.go" {
		t.Fatalf("model asked for %v, want only keepalive_synth.go", got)
	}
	for f, o := range byFile(out) {
		switch {
		case f == "keepalive_synth.go" && o.Reason != "gen_hash":
			t.Errorf("keepalive: %+v", o)
		case strings.HasSuffix(f, "_synth.go") && f != "keepalive_synth.go" && (o.Reason != "skeleton_hash" || o.Calls != 0):
			t.Errorf("%s: %+v", f, o)
		}
	}
}

// failingShapes fails the n-th shape write.
type failingShapes struct {
	records.ShapeStore
	n, calls int
}

func (f *failingShapes) SetShape(id string, shape []any, reason, author string) (bool, error) {
	f.calls++
	if f.calls == f.n {
		return false, errors.New("disk full")
	}
	return f.ShapeStore.SetShape(id, shape, reason, author)
}

// An import interrupted part-way stops the run before anything is rendered,
// says so, and the next run completes it to the same records a clean import
// gives.
func TestSkeletonImportInterruptedIsCompletedByTheNextRun(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	s, err := records.OpenDir(cfg.Repo)
	if err != nil {
		t.Fatal(err)
	}
	ws := &failingShapes{ShapeStore: s.(records.ShapeStore), n: 3}
	err = cfg.importSkeleton(ws, acc)
	if err == nil || !strings.Contains(err.Error(), "re-run to complete") || !strings.Contains(err.Error(), "revising 2 record(s)") {
		t.Fatalf("want an interrupted-import error naming the progress: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, "running.go")); err == nil {
		t.Fatal("nothing may be rendered from a partial import")
	}
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	shaped := 0
	for _, a := range acc {
		if p, _ := s.Current(a.ID); len(p.Shape) > 0 {
			shaped++
		}
	}
	if shaped != len(cfg.Skeleton.Files) {
		t.Fatalf("%d records carry a shape after the completing run, want %d", shaped, len(cfg.Skeleton.Files))
	}
}

// A record that cannot be read stops the import before any record is revised.
func TestSkeletonImportChecksEveryRecordFirst(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	acc := accept(t, cfg, spec)
	acc[len(acc)-1].ID = "prov-2026-deadbeef"
	s, _ := records.OpenDir(cfg.Repo)
	ws := &failingShapes{ShapeStore: s.(records.ShapeStore)}
	if err := cfg.importSkeleton(ws, acc[:]); err == nil || ws.calls != 0 {
		t.Fatalf("err=%v, %d shape writes; want an error before any write", err, ws.calls)
	}
}
