package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
)

// A proposal is checked in a scratch tree and repo: it renders, seeds and
// builds, and the real repo and tree are never touched.
func TestRenderScratchBuildsAndTouchesNothing(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	cfg.Lang.Name = "" // default Go profile
	root, cleanup, err := RenderScratch(context.Background(), cfg, drafter.Drafts(spec), nil, *cfg.Skeleton)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "x"), ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seeded scratch tree does not build: %v\n%s", err, out)
	}
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("cleanup left the scratch tree behind")
	}
	if _, err := os.Stat(cfg.Root); !os.IsNotExist(err) {
		t.Error("the real tree was created")
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, ".graft", "accepted.json")); !os.IsNotExist(err) {
		t.Error("the real repo got an index")
	}
}

func TestRenderScratchRejectsABrokenProposal(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	sk := *cfg.Skeleton
	sk.Files = append([]recording.File(nil), sk.Files...)
	sk.Files[0].Section = "freeq-lite › Nowhere"
	if _, _, err := RenderScratch(context.Background(), cfg, drafter.Drafts(spec), nil, sk); err == nil {
		t.Fatal("a proposal naming a section no record covers must fail")
	}
}

// DoD: acceptance is the existing import. A proposal written as JSON by the
// draft command, read back by --render's loader, lands in the records' shapes,
// and a later render from those records (the skeleton they spell) is a no-op.
func TestAcceptedProposalIsImportedAndLaterRenderUsesIt(t *testing.T) {
	cfg, m, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	b, err := json.Marshal(cfg.Skeleton) // what graft draft --skeleton writes
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := recording.LoadSkeleton(b)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Skeleton = &proposal
	acc := accept(t, cfg, spec)
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	store, _ := records.OpenDir(cfg.Repo)
	var pins []records.Pin
	for _, a := range acc {
		p, _ := store.Current(a.ID)
		pins = append(pins, p)
	}
	fromRecords, err := recording.SkeletonOf("", pins)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromRecords.Files) != len(proposal.Files) {
		t.Fatalf("records spell %d files, proposal had %d", len(fromRecords.Files), len(proposal.Files))
	}
	v1, calls := versions(t, cfg.Repo, acc), len(m.prompts)
	cfg.Skeleton = &fromRecords // --render now needs no skeleton file's say-so
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatal(err)
	}
	if !equalInts(v1, versions(t, cfg.Repo, acc)) || len(m.prompts) != calls {
		t.Error("rendering from the records' own shapes changed versions or called the model")
	}
}

// A section accepted with files {"One": "pkg/one.go"} keeps that file as its
// affected_scope. SectionFiles reads it from the record (never recomputing it),
// a skeleton drafted there checks in scratch, and the real import then succeeds.
func TestSectionAcceptedWithFilesIsDraftedAndImportedAtItsRecordsFile(t *testing.T) {
	cfg, _, _ := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	const spec = "# app\n\n## One\n\n- The server must accept a nick\n\n## Two\n\n- The server must answer a PING with a PONG\n"
	drafts := drafter.Drafts(spec)
	if _, err := AcceptWith(cfg.Repo, drafts, "t", time.Now(), AcceptOptions{Ext: ".go", Files: map[string]string{"One": "pkg/one.go"}}); err != nil {
		t.Fatal(err)
	}
	placed, err := SectionFiles(cfg.Repo, drafts, ".go")
	if err != nil {
		t.Fatal(err)
	}
	if placed["app › One"] != "pkg/one.go" || placed["app › Two"] != "two.go" {
		t.Fatalf("placed = %v: a record's file is read from the record, a new section's is derived", placed)
	}
	sk, err := recording.LoadSkeleton([]byte(`{"files":[
	 {"path":"pkg/one.go","section":"app › One","stubs":[{"name":"Accept","params":[{"name":"nick","type":"string"}],"results":["bool"],"doc":"accepts a nick"}]},
	 {"path":"two.go","section":"app › Two","stubs":[{"name":"main","doc":"serves"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := RenderScratch(context.Background(), cfg, drafts, placed, sk)
	if err != nil {
		t.Fatalf("a skeleton at the record's file must check in scratch: %v", err)
	}
	cleanup()
	// the real import: shapes land in the records, at their files
	cfg.Spec, cfg.Skeleton = spec, &sk
	acc, err := AcceptWith(cfg.Repo, drafts, "t", time.Now(), AcceptOptions{Ext: ".go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cfg, acc); err != nil {
		t.Fatalf("import at the record's file failed: %v", err)
	}
	store, _ := records.OpenDir(cfg.Repo)
	for _, a := range acc {
		if p, _ := store.Current(a.ID); len(p.Shape) != 1 {
			t.Errorf("%s: shape %v", a.Entry.Section, p.Shape)
		}
	}
	// and the derived path is what a scratch record at pkg/one.go refuses
	bad := sk
	bad.Files = append([]recording.File(nil), sk.Files...)
	bad.Files[0].Path = "one.go"
	if _, _, err := RenderScratch(context.Background(), cfg, drafts, placed, bad); err == nil {
		t.Error("a skeleton at the derived path must not check when the record is at pkg/one.go")
	}
}
