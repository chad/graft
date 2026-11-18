package converge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/evidence"
)

// fakeGate passes when the candidate tree contains want in file.
type fakeGate struct {
	file, want string
	kind       evidence.Kind
	ran        *int
	touch      string // a file to create in the tree it runs in
}

func (g fakeGate) Name() string        { return "fake" }
func (g fakeGate) Kind() evidence.Kind { return g.kind }
func (g fakeGate) Run(_ context.Context, dir string) (evidence.Verdict, string, error) {
	if g.ran != nil {
		*g.ran++
	}
	if g.touch != "" {
		os.WriteFile(filepath.Join(dir, g.touch), []byte("x"), 0o644)
	}
	b, _ := os.ReadFile(filepath.Join(dir, g.file))
	if strings.Contains(string(b), g.want) {
		return evidence.Pass, "found", nil
	}
	return evidence.Fail, "missing", nil
}

func setup(t *testing.T, gates ...Gate) (Converger, string) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0o644)
	log, err := evidence.Open(filepath.Join(t.TempDir(), "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return Converger{Root: root, Log: log, Gates: gates,
		Policy: evidence.Policy{"low": {evidence.TestRun}, "high": {evidence.TestRun, evidence.HumanSignoff}}}, root
}

func cand(body string) Candidate {
	return Candidate{Path: "app.go", RecordID: "prov-1", Version: 2, GenHash: "g2", SpecHash: "s1", Tier: "low", Body: body}
}

func read(t *testing.T, root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "app.go"))
	return string(b)
}

func TestAcceptedWhenGatePasses(t *testing.T) {
	c, root := setup(t, fakeGate{file: "app.go", want: "func Add", kind: evidence.TestRun})
	d, err := c.Converge(context.Background(), cand("func Add(a, b int) int { return a + b }"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Applied || d.Guarantee != "checked" {
		t.Fatalf("decision %+v", d)
	}
	if !strings.Contains(read(t, root), "func Add") {
		t.Fatal("not applied to the tree")
	}
	e := d.Evidence[0]
	want := evidence.Pin{RecordID: "prov-1", Version: 2, GenHash: "g2", SpecHash: "s1",
		RegionHash: HashBody("func Add(a, b int) int { return a + b }\n")}
	if e.Pin != want || e.Verdict != evidence.Pass {
		t.Fatalf("evidence %+v", e)
	}
	rs, err := ReadRegions(root)
	if err != nil || len(rs) != 1 || rs[0].Edited() || rs[0].Owner.Record != "prov-1" {
		t.Fatalf("regions %+v %v", rs, err)
	}
}

func TestRejectedWhenGateFails(t *testing.T) {
	c, root := setup(t, fakeGate{file: "app.go", want: "func Add", kind: evidence.TestRun})
	d, err := c.Converge(context.Background(), cand("func Sub(a, b int) int { return a - b }"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Applied || d.Result.Satisfied || len(d.Result.Failing) != 1 {
		t.Fatalf("decision %+v", d)
	}
	if read(t, root) != "package app\n" {
		t.Fatal("a rejected candidate reached the tree")
	}
	if d.Evidence[0].Verdict != evidence.Fail {
		t.Fatal("the failure was not recorded")
	}
}

func TestTierNeedsMoreThanTheGatesProvide(t *testing.T) {
	c, root := setup(t, fakeGate{file: "app.go", want: "func", kind: evidence.TestRun})
	k := cand("func F() {}")
	k.Tier = "high"
	d, err := c.Converge(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if d.Applied || len(d.Result.Missing) != 1 || d.Result.Missing[0] != evidence.HumanSignoff {
		t.Fatalf("decision %+v", d)
	}
	if read(t, root) != "package app\n" {
		t.Fatal("applied without sign-off")
	}
}

func TestGatesRunInScratchNotTheTree(t *testing.T) {
	c, root := setup(t, fakeGate{file: "app.go", want: "func", kind: evidence.TestRun, touch: "build-artifact"})
	if _, err := c.Converge(context.Background(), cand("func F() {}")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "build-artifact")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a gate wrote into the real tree")
	}
}

func TestRefusesOverHandEditedRegion(t *testing.T) {
	ran := 0
	c, root := setup(t, fakeGate{file: "app.go", want: "func", kind: evidence.TestRun, ran: &ran})
	if _, err := c.Converge(context.Background(), cand("func F() {}")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "app.go")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "func F() {}", "func F() { panic(1) }", 1)), 0o644)
	ran = 0
	_, err := c.Converge(context.Background(), cand("func F() { return }"))
	if !errors.Is(err, ErrHandEdited) {
		t.Fatalf("err = %v", err)
	}
	if ran != 0 {
		t.Fatal("gates ran before the refusal")
	}
}

func TestRegenerationReplacesOnlyItsOwnRegion(t *testing.T) {
	c, root := setup(t, fakeGate{file: "app.go", want: "func", kind: evidence.TestRun})
	ctx := context.Background()
	c.Converge(ctx, cand("func F() {}"))
	other := cand("func G() {}")
	other.RecordID = "prov-2"
	c.Converge(ctx, other)
	if _, err := c.Converge(ctx, cand("func F() { _ = 1 }")); err != nil {
		t.Fatal(err)
	}
	got := read(t, root)
	if strings.Count(got, beginTag) != 2 || !strings.Contains(got, "func G() {}") || !strings.Contains(got, "_ = 1") {
		t.Fatalf("tree:\n%s", got)
	}
}

func TestUnterminatedRegionIsAnError(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "x.go"), []byte("// "+beginTag+` {"record":"r","tier":"low","body":"x"}`+"\nfunc F() {}\n"), 0o644)
	if _, err := ReadRegions(root); !errors.Is(err, ErrUnterminated) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownTierIsAnError(t *testing.T) {
	c, _ := setup(t)
	k := cand("func F() {}")
	k.Tier = "hgih"
	if _, err := c.Converge(context.Background(), k); !errors.Is(err, evidence.ErrUnknownTier) {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandGate(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cases := []struct {
		argv []string
		to   time.Duration
		want evidence.Verdict
	}{
		{[]string{"true"}, 0, evidence.Pass},
		{[]string{"false"}, 0, evidence.Fail},
		{[]string{"sleep", "5"}, 100 * time.Millisecond, evidence.Inconclusive},
	}
	for _, c := range cases {
		v, _, err := CommandGate{Label: c.argv[0], Argv: c.argv, As: evidence.TestRun, Timeout: c.to}.Run(ctx, dir)
		if err != nil || v != c.want {
			t.Errorf("%v: %s %v, want %s", c.argv, v, err, c.want)
		}
	}
	if _, _, err := (CommandGate{Argv: []string{"no-such-binary-graft"}}).Run(ctx, dir); err == nil {
		t.Error("a gate that cannot start must be an error, not a verdict")
	}
}

// End to end with a real toolchain: the gate is `go vet` on the candidate.
func TestRealGoGate(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	c, root := setup(t, CommandGate{Label: "go vet", Argv: []string{"go", "vet", "./..."}, As: evidence.TestRun, Timeout: time.Minute})
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module app\n\ngo 1.22\n"), 0o644)
	ctx := context.Background()
	if d, err := c.Converge(ctx, cand("func Broken( { }")); err != nil || d.Applied {
		t.Fatalf("broken code applied: %+v %v", d, err)
	}
	if d, err := c.Converge(ctx, cand("func Add(a, b int) int { return a + b }")); err != nil || !d.Applied {
		t.Fatalf("valid code rejected: %+v %v", d, err)
	}
}

func TestNoGatesIsRefused(t *testing.T) {
	c, root := setup(t)
	c.Policy["none"] = []evidence.Kind{}
	k := cand("func F() {}")
	k.Tier = "none"
	if _, err := c.Converge(context.Background(), k); !errors.Is(err, ErrNoGates) {
		t.Fatalf("err = %v", err)
	}
	if read(t, root) != "package app\n" {
		t.Fatal("applied with nothing checked")
	}
}

// changingGate edits the REAL tree while it runs, as a person or another writer might.
type changingGate struct{ root string }

func (g changingGate) Name() string        { return "changer" }
func (g changingGate) Kind() evidence.Kind { return evidence.TestRun }
func (g changingGate) Run(context.Context, string) (evidence.Verdict, string, error) {
	os.WriteFile(filepath.Join(g.root, "app.go"), []byte("package app\n// edited meanwhile\n"), 0o644)
	return evidence.Pass, "", nil
}

func TestChangeDuringConvergenceIsNotOverwritten(t *testing.T) {
	c, root := setup(t)
	c.Gates = []Gate{changingGate{root}}
	d, err := c.Converge(context.Background(), cand("func F() {}"))
	if !errors.Is(err, ErrChanged) || d.Applied {
		t.Fatalf("err = %v, applied = %v", err, d.Applied)
	}
	if !strings.Contains(read(t, root), "edited meanwhile") {
		t.Fatal("the concurrent edit was clobbered")
	}
}

func TestCandidatePathMustStayInsideRoot(t *testing.T) {
	c, root := setup(t, fakeGate{file: "app.go", want: "", kind: evidence.TestRun})
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "link"))
	os.Symlink(filepath.Join(outside, "f.go"), filepath.Join(root, "f.go"))
	for _, p := range []string{"../escape.go", "/etc/x.go", "link/x.go", "f.go", ""} {
		k := cand("func F() {}")
		k.Path = p
		if _, err := c.Converge(context.Background(), k); !errors.Is(err, ErrBadPath) {
			t.Errorf("%q: err = %v", p, err)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the root: %v", entries)
	}
}

func TestScratchKeepsSymlinksAsSymlinks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0o644)
	os.Symlink("a", filepath.Join(src, "b"))
	if err := copyTree(src, dst, nil); err != nil {
		t.Fatal(err)
	}
	if to, err := os.Readlink(filepath.Join(dst, "b")); err != nil || to != "a" {
		t.Fatalf("symlink not preserved: %q %v", to, err)
	}
}

type verdictGate struct {
	kind evidence.Kind
	v    evidence.Verdict
}

func (g verdictGate) Name() string        { return string(g.kind) + ":" + string(g.v) }
func (g verdictGate) Kind() evidence.Kind { return g.kind }
func (g verdictGate) Run(context.Context, string) (evidence.Verdict, string, error) {
	return g.v, "", nil
}

// Review on #6 (1): a failing gate is not masked by a later passing gate of the same kind.
func TestSameKindFailIsNotMasked(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Fail}, verdictGate{evidence.TestRun, evidence.Pass})
	d, err := c.Converge(context.Background(), cand("func F() {}"))
	if err != nil || d.Applied {
		t.Fatalf("applied despite a failing gate: %+v %v", d, err)
	}
	if read(t, root) != "package app\n" {
		t.Fatal("tree changed")
	}
}

// Every gate must pass, even one whose kind the tier does not require.
func TestAnyFailingGateBlocks(t *testing.T) {
	c, _ := setup(t, verdictGate{evidence.TestRun, evidence.Pass}, verdictGate{evidence.StaticAnalysis, evidence.Fail})
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
}

// Review on #6 (2): an earlier run's executable evidence never stands in for this run.
func TestEarlierRunEvidenceIsNotReused(t *testing.T) {
	c, _ := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || !d.Applied {
		t.Fatalf("run 1: %+v %v", d, err)
	}
	c.Gates = []Gate{verdictGate{evidence.Eval, evidence.Fail}} // no TestRun this time
	k := cand("func F() {}")
	_, err := c.Converge(context.Background(), k)
	if !errors.Is(err, ErrUncovered) {
		t.Fatalf("err = %v, want ErrUncovered", err)
	}
}

// Non-executable kinds (a human sign-off on exactly this pin) do come from the log.
func TestHumanSignoffFromLogCounts(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	k := cand("func F() {}")
	k.Tier = "high"
	pin := evidence.Pin{RecordID: k.RecordID, Version: k.Version, GenHash: k.GenHash, SpecHash: k.SpecHash,
		RegionHash: HashBody("func F() {}\n")}
	if _, err := c.Log.Append(evidence.Evidence{Kind: evidence.HumanSignoff, Verdict: evidence.Pass, Pin: pin, By: "did:plc:x"}); err != nil {
		t.Fatal(err)
	}
	if d, err := c.Converge(context.Background(), k); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if !strings.Contains(read(t, root), "func F() {}") {
		t.Fatal("not applied")
	}
}

// Review on #6 (3): marker tags in a body are refused, even inside a string literal.
func TestMarkerInBodyIsRefused(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	for _, body := range []string{`var s = "graft:synth:end"`, "// graft:synth:begin {}"} {
		if _, err := c.Converge(context.Background(), cand(body)); !errors.Is(err, ErrMarkerInBody) {
			t.Errorf("%q: err = %v", body, err)
		}
	}
	if read(t, root) != "package app\n" {
		t.Fatal("tree changed")
	}
}

func TestGateRunErrorAppliesNothing(t *testing.T) {
	c, root := setup(t)
	c.Gates = []Gate{CommandGate{Label: "missing", Argv: []string{"no-such-binary-graft"}, As: evidence.TestRun}}
	d, err := c.Converge(context.Background(), cand("func F() {}"))
	if err == nil || d.Applied || read(t, root) != "package app\n" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestOtherFileChangingDuringGatesBlocks(t *testing.T) {
	c, root := setup(t)
	c.Gates = []Gate{otherFileGate{root}}
	d, err := c.Converge(context.Background(), cand("func F() {}"))
	if !errors.Is(err, ErrChanged) || d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
}

type otherFileGate struct{ root string }

func (g otherFileGate) Name() string        { return "other" }
func (g otherFileGate) Kind() evidence.Kind { return evidence.TestRun }
func (g otherFileGate) Run(context.Context, string) (evidence.Verdict, string, error) {
	os.WriteFile(filepath.Join(g.root, "other.go"), []byte("package app\n"), 0o644)
	return evidence.Pass, "", nil
}

func TestModeIsPreserved(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	os.Chmod(filepath.Join(root, "app.go"), 0o600)
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "app.go")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// Review on #6 (5): an absolute symlink into the real tree is rewritten into scratch.
func TestAbsoluteSymlinkIntoRootIsRewritten(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0o644)
	abs, _ := filepath.Abs(filepath.Join(src, "a"))
	os.Symlink(abs, filepath.Join(src, "b"))
	if err := copyTree(src, dst, nil); err != nil {
		t.Fatal(err)
	}
	to, _ := os.Readlink(filepath.Join(dst, "b"))
	if strings.HasPrefix(to, src) || to != filepath.Join(dst, "a") {
		t.Fatalf("link points at %q", to)
	}
}

// Review on #6 (6): a timeout kills the gate's children too.
func TestTimeoutKillsChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	start := time.Now()
	v, _, err := CommandGate{Label: "sleepy", Argv: []string{"sh", "-c", "sleep 30 & sleep 30; wait"}, As: evidence.TestRun,
		Timeout: 200 * time.Millisecond}.Run(context.Background(), t.TempDir())
	if err != nil || v != evidence.Inconclusive {
		t.Fatalf("%s %v", v, err)
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Fatalf("took %s: children outlived the timeout", el)
	}
}

func TestMarkerParsingIsStrict(t *testing.T) {
	ok := `{"record":"r","tier":"low","body":"` + HashBody("x\n") + `"}`
	cases := map[string]struct {
		src  string
		want error
	}{
		"end without begin":  {"// graft:synth:end {\"record\":\"r\"}\n", ErrBadMarker},
		"empty owner":        {"// graft:synth:begin {}\nx\n// graft:synth:end {\"record\":\"\"}\n", ErrBadMarker},
		"end names another":  {"// graft:synth:begin " + ok + "\nx\n// graft:synth:end {\"record\":\"q\"}\n", ErrBadMarker},
		"duplicate record":   {"// graft:synth:begin " + ok + "\nx\n// graft:synth:end {\"record\":\"r\"}\n// graft:synth:begin " + ok + "\nx\n// graft:synth:end {\"record\":\"r\"}\n", ErrDuplicateRegion},
		"tag inside code":    {`const t = "graft:synth:begin {}"` + "\n", nil},
		"block comment form": {"/* graft:synth:begin " + ok + " */\nx\n/* graft:synth:end {\"record\":\"r\"} */\n", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parse("f", []byte(c.src))
			if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// Graft's own source mentions the tags in code; that must not read as markers.
func TestReadRegionsOnGraftItself(t *testing.T) {
	if _, err := ReadRegions("../.."); err != nil {
		t.Fatal(err)
	}
}

// Review on #6 round 2: a region in a new file, and in a new directory, applies.
func TestNewFileAndNewDirectoryApply(t *testing.T) {
	for _, p := range []string{"b.go", "pkg/deep/c.go"} {
		t.Run(p, func(t *testing.T) {
			c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
			k := cand("func F() {}")
			k.Path = p
			d, err := c.Converge(context.Background(), k)
			if err != nil || !d.Applied {
				t.Fatalf("%+v %v", d, err)
			}
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil || !strings.Contains(string(b), "func F() {}") {
				t.Fatalf("%q %v", b, err)
			}
		})
	}
}

// A rejected candidate for a new file leaves nothing behind.
func TestRejectedNewFileLeavesNothing(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Fail})
	k := cand("func F() {}")
	k.Path = "pkg/new.go"
	if d, err := c.Converge(context.Background(), k); err != nil || d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("left a directory behind")
	}
}

// Review on #6 round 2: a planted symlink at a predictable temp name is never followed.
func TestPlantedTempSymlinkIsNotFollowed(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("keep"), 0o644)
	for _, name := range []string{"app.go.graft-tmp", ".graft-0.tmp", "app.go.tmp"} {
		os.Symlink(outside, filepath.Join(root, name))
	}
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatalf("wrote through a planted symlink: %q", b)
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".graft-") && strings.HasSuffix(e.Name(), ".tmp") && e.Type().IsRegular() {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestCheckRenderedRejectsMismatch(t *testing.T) {
	k := cand("func F() {}")
	next, err := renderRegion(k.Path, []byte("package app\n"), "//", Owner{Record: k.RecordID, Tier: "low"}, k.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRendered(k, next, HashBody("func F() {}\n")); err != nil {
		t.Fatalf("good render rejected: %v", err)
	}
	if err := checkRendered(k, next, HashBody("something else\n")); err == nil {
		t.Fatal("a render that does not hash to its pin was accepted")
	}
	k2 := k
	k2.RecordID = "prov-missing"
	if err := checkRendered(k2, next, HashBody("func F() {}\n")); err == nil {
		t.Fatal("a render with no region for the record was accepted")
	}
}

// A symlink planted in the target's parent while gates run is refused at apply.
func TestSymlinkAppearingDuringGatesIsRefused(t *testing.T) {
	c, root := setup(t)
	os.MkdirAll(filepath.Join(root, "pkg"), 0o755)
	c.Gates = []Gate{swapDirGate{root, t.TempDir()}}
	k := cand("func F() {}")
	k.Path = "pkg/x.go"
	_, err := c.Converge(context.Background(), k)
	if !errors.Is(err, ErrBadPath) && !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v", err)
	}
}

type swapDirGate struct{ root, outside string }

func (g swapDirGate) Name() string        { return "swap" }
func (g swapDirGate) Kind() evidence.Kind { return evidence.TestRun }
func (g swapDirGate) Run(context.Context, string) (evidence.Verdict, string, error) {
	os.RemoveAll(filepath.Join(g.root, "pkg"))
	os.Symlink(g.outside, filepath.Join(g.root, "pkg"))
	return evidence.Pass, "", nil
}

// Nothing of graft's own is left in the tree after a run: no lock, no temp.
func TestApplyLeavesNoArtifactsInTree(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "app.go" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("tree has %v", names)
	}
}

func TestMkdirAllTrackedReportsOnlyWhatItMade(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "a"), 0o755)
	made, err := mkdirAllTracked(root, filepath.Join(root, "a", "b", "c"))
	if err != nil || len(made) != 2 || made[0] != filepath.Join(root, "a", "b") {
		t.Fatalf("%v %v", made, err)
	}
}

func TestPlantedLockSymlinkIsRefused(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	lp, _ := lockPath(root)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	if err := os.Symlink(victim, lp); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lp)
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err == nil || d.Applied {
		t.Fatalf("followed a planted lock symlink: %+v %v", d, err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("victim modified")
	}
}
