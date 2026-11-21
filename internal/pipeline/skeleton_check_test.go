package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
)

// A proposal is checked in a scratch tree and repo: it renders, seeds and
// builds, and the real repo and tree are never touched.
func TestRenderScratchBuildsAndTouchesNothing(t *testing.T) {
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	cfg.Lang.Name = "" // default Go profile
	root, cleanup, err := RenderScratch(context.Background(), cfg, drafter.Drafts(spec), *cfg.Skeleton)
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
	if _, _, err := RenderScratch(context.Background(), cfg, drafter.Drafts(spec), sk); err == nil {
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
