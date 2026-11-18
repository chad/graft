package recording

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// writeRecords runs the drafter on the freeq-lite spec and writes each draft
// as a provenance record under dir/provenance, ids derived from the section so
// the result is deterministic. It returns the records as records.OpenDir reads
// them.
func writeRecords(t *testing.T, dir string, sk Skeleton) []Rec {
	t.Helper()
	scope := map[string][]string{}
	for _, f := range sk.Files {
		scope[f.Section] = append(scope[f.Section], f.Path)
	}
	spec, err := os.ReadFile("testdata/freeq-lite.md")
	if err != nil {
		t.Fatal(err)
	}
	drafts := drafter.Drafts(string(spec))
	if len(drafts) != 7 {
		t.Fatalf("drafter produced %d blueprints, want 7", len(drafts))
	}
	if err := os.MkdirAll(filepath.Join(dir, "provenance"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := records.OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Rec
	for _, d := range drafts {
		if !d.Receipt.Validated {
			t.Fatalf("%s: draft does not validate: %s", d.Section, d.Receipt.Reason)
		}
		sum := sha256.Sum256([]byte(d.Section))
		id := "prov-2026-" + hex.EncodeToString(sum[:4])
		var intent strings.Builder
		for _, s := range d.Statements {
			intent.WriteString(s.Text + "\n")
		}
		y := fmt.Sprintf(`id: %s
title: %s
type: imprint
implements: prov-2026-parent
status: open
version: 1
reason: ""
created_at: "2026-11-18"
author: fixture@example.com
intent: %s
constraints: []
affected_scope: %s
forbidden_scope: []
related: []
sealed_at_sha: ""
associated_specs: []
associated_traces: []
monitors: []
tags: []
`, id, strconv.Quote(d.Section), strconv.Quote(intent.String()), flow(scope[d.Section]))
		if err := os.WriteFile(filepath.Join(dir, "provenance", id+".yml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := st.Current(id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, Rec{Pin: p, Section: d.Section})
	}
	return out
}

func flow(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func freeqInput(t *testing.T, dir string) Input {
	t.Helper()
	b, err := os.ReadFile("../../examples/freeq-lite/skeleton.json")
	if err != nil {
		t.Fatal(err)
	}
	sk, err := LoadSkeleton(b)
	if err != nil {
		t.Fatal(err)
	}
	return Input{Records: writeRecords(t, dir, sk), Skeleton: sk, Tier: "low", Contract: sedumContract(t, "go")}
}

func recordingBytes(t *testing.T, in Input) []byte {
	t.Helper()
	p, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, p.Recording); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPlanIsDeterministic(t *testing.T) {
	in := freeqInput(t, t.TempDir())
	a := recordingBytes(t, in)
	for i := 0; i < 5; i++ {
		if b := recordingBytes(t, in); !bytes.Equal(a, b) {
			t.Fatal("recordings differ between runs")
		}
	}
	rev := freeqInput(t, t.TempDir())
	for i, j := 0, len(rev.Records)-1; i < j; i, j = i+1, j-1 {
		rev.Records[i], rev.Records[j] = rev.Records[j], rev.Records[i]
	}
	if !bytes.Equal(a, recordingBytes(t, rev)) {
		t.Fatal("pin order changed the recording")
	}
}

func TestPlanRejectsBadInput(t *testing.T) {
	in := freeqInput(t, t.TempDir())
	in.Records = in.Records[:2]
	if _, err := Plan(in); err == nil {
		t.Fatal("file naming an unknown record must be an error")
	}
	if _, err := Plan(Input{Records: []Rec{{Pin: records.Pin{RecordID: "x"}, Section: "s"}}, Skeleton: Skeleton{Files: []File{{Path: "../x.go", Section: "s"}}}}); err == nil {
		t.Fatal("escaping path must be an error")
	}
}

// copyGenerators puts sedum's go generator at <dir>/generators/go. The release
// binary does not carry it, so it comes from SEDUM_GENERATORS or a sedum
// checkout at ~/sedum.
func copyGenerators(t *testing.T, dir string) {
	t.Helper()
	src := os.Getenv("SEDUM_GENERATORS")
	if src == "" {
		home, _ := os.UserHomeDir()
		src = filepath.Join(home, "sedum", "generators")
	}
	if _, err := os.Stat(filepath.Join(src, "go")); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("sedum generators not found at %s", src)
		}
		t.Skip("sedum generators not found; set SEDUM_GENERATORS")
	}
	if out, err := exec.Command("cp", "-r", src, filepath.Join(dir, "generators")).CombinedOutput(); err != nil {
		t.Fatalf("copy generators: %v %s", err, out)
	}
}

// grown renders the plan with real sedum into a fresh module and returns the dir.
func grown(t *testing.T) (string, render.Sedum, Planned, Input) {
	t.Helper()
	bin, err := exec.LookPath("sedum")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("sedum must be installed in CI")
		}
		t.Skip("sedum not on PATH")
	}
	dir := t.TempDir()
	in := freeqInput(t, dir)
	p, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module freeqlite\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, p.Recording); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(dir, "recording.json")
	if err := os.WriteFile(rec, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	copyGenerators(t, dir)
	s := render.Exec(bin)
	if err := render.Render(context.Background(), s, dir, rec); err != nil {
		t.Fatal(err)
	}
	return dir, s, p, in
}

// dropGenerators removes the generator templates, which are not Go packages,
// so that go build ./... and gofmt see only what sedum grew.
func dropGenerators(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(dir, "generators")); err != nil {
		t.Fatal(err)
	}
}

// seed lays each region's panicking placeholder down. The package must build
// with only the seeds in place, before any convergence has run.
func seed(t *testing.T, dir string, p Planned) {
	t.Helper()
	for _, r := range p.Regions {
		if err := os.WriteFile(filepath.Join(dir, r.Path), r.Seed, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goBuild(t, dir)
}

func goBuild(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

// bodyOf is the seed without its marker lines: the text between the markers.
func bodyOf(r Region) string {
	var sb strings.Builder
	for _, l := range strings.SplitAfter(string(r.Seed), "\n") {
		if !strings.Contains(l, "graft:synth") {
			sb.WriteString(l)
		}
	}
	return sb.String()
}

// fill converges every region through the real Converger, gated on go build,
// with a body that differs from the seed.
func fill(t *testing.T, dir string, p Planned, in Input) {
	t.Helper()
	log, err := evidence.Open(filepath.Join(t.TempDir(), "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := records.OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := converge.Converger{Root: dir, Records: st, Log: log,
		Policy: evidence.Policy{"low": {evidence.TestRun}},
		Gates:  []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.TestRun, Timeout: time.Minute}}}
	for _, r := range p.Regions {
		pin, err := st.Current(r.Owner.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ReplaceAll(bodyOf(r), "graft: not converged", "graft: test body")
		d, err := c.Converge(context.Background(), converge.Candidate{Path: r.Path, RecordID: pin.RecordID,
			Version: pin.Version, GenHash: pin.GenHash, SpecHash: pin.SpecHash, Tier: r.Tier, Body: body})
		if err != nil || !d.Applied {
			t.Fatalf("converge %s: applied=%v err=%v %+v", r.Path, d.Applied, err, d.Result)
		}
	}
}

func gofmtClean(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("gofmt", "-l", dir).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("gofmt: %v %s", err, out)
	}
}

func TestRecordingGrowsWithRealSedum(t *testing.T) {
	dir, s, p, in := grown(t)
	dropGenerators(t, dir)
	seed(t, dir, p)
	fill(t, dir, p, in)
	gofmtClean(t, dir)
	goBuild(t, dir)
	// after convergence the tree is still current, and a regrow keeps it so
	copyGenerators(t, dir)
	if err := s.GrowCheck(context.Background(), dir); err != nil {
		t.Fatalf("grow --check after fill: %v", err)
	}
	rec := filepath.Join(dir, "recording.json")
	if err := render.Render(context.Background(), s, dir, rec); err != nil {
		t.Fatalf("regrow after fill: %v", err)
	}
	dropGenerators(t, dir)
	goBuild(t, dir)
	for _, r := range p.Regions {
		b, _ := os.ReadFile(filepath.Join(dir, r.Path))
		if !strings.Contains(string(b), "graft: test body") {
			t.Errorf("%s: regrow lost the converged body", r.Path)
		}
	}
}

func TestGrowCheckCleanAfterGrow(t *testing.T) {
	dir, s, p, in := grown(t)
	if len(in.Skeleton.Files) != 7 {
		t.Fatalf("skeleton has %d files", len(in.Skeleton.Files))
	}
	if err := s.GrowCheck(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// the check must actually look at files: a hand edit to a sedum file fails it
	f := filepath.Join(dir, in.Skeleton.Files[0].Path)
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append(b, []byte("\nvar handEdit = 1\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.GrowCheck(context.Background(), dir); err == nil {
		t.Fatal("grow --check passed over a hand-edited sedum file")
	}
	_ = p
}

func TestRegionsOwnedByRecords(t *testing.T) {
	dir, _, p, in := grown(t)
	want := map[string]records.Pin{}
	for _, r := range in.Records {
		want[r.Pin.RecordID] = r.Pin
	}
	regs, err := render.ReadMarkers(dir)
	if err != nil || len(regs) == 0 {
		t.Fatalf("markers: %v (%d)", err, len(regs))
	}
	for _, r := range regs {
		pin, ok := want[r.Owner.RecordID]
		// v2 markers carry no version (render.go), so only record and gen_hash are checked
		if !ok || r.Owner.GenHash != pin.GenHash {
			t.Errorf("%s: region owned by %+v, want one of the planned records with its gen_hash", r.Path, r.Owner)
		}
	}
	owners := map[string]bool{}
	for _, r := range regs {
		owners[r.Path] = true
	}
	for _, f := range in.Skeleton.Files {
		if !owners[f.Path] {
			t.Errorf("%s has no sedum region", f.Path)
		}
	}
	for _, r := range p.Regions {
		if want[r.Owner.RecordID].GenHash != r.Owner.GenHash {
			t.Errorf("convergence region owner %+v does not match its record", r.Owner)
		}
	}
}

var stubBody = regexp.MustCompile(`(?s)\{\n\t(return )?(\w+\.)?\w+Body\([^)]*\)\n\}`)
var stubFunc = regexp.MustCompile(`(?s)\nfunc [^\n]*\{\n.*?\n\}\n`)

func TestHandlerBodiesAreConvergenceRegions(t *testing.T) {
	dir, _, p, in := grown(t)
	if len(p.Regions) != 7 {
		t.Fatalf("want one convergence region per file, got %d", len(p.Regions))
	}
	sedumFiles := map[string]string{}
	for _, f := range in.Skeleton.Files {
		sedumFiles[f.Path] = f.Section
	}
	for _, r := range p.Regions {
		if _, owned := sedumFiles[r.Path]; owned || !strings.HasSuffix(r.Path, "_synth.go") {
			t.Errorf("%s: region file must be a <stem>_synth.go sedum does not own", r.Path)
		}
		if len(r.Declares) == 0 {
			t.Errorf("%s: region declares nothing", r.Path)
		}
		if n := strings.Count(string(r.Seed), "graft:synth"); n != 2 {
			t.Errorf("%s: seed has %d marker lines, want one region (2)", r.Path, n)
		}
		if _, err := os.Stat(filepath.Join(dir, r.Path)); err == nil {
			t.Errorf("%s: sedum rendered the synth file", r.Path)
		}
	}
	// every stub in every sedum-owned file is a pure delegation to a helper the region declares
	var declared []string
	for _, r := range p.Regions {
		for _, d := range r.Declares {
			declared = append(declared, regexp.MustCompile(`\w+Body`).FindString(d))
		}
	}
	for _, f := range in.Skeleton.Files {
		src, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			t.Fatal(err)
		}
		if len(f.Stubs) == 0 {
			continue
		}
		n := 0
		for _, m := range stubFunc.FindAll(src, -1) {
			if !strings.Contains(string(m), "Body(") {
				continue
			}
			n++
			if !stubBody.Match(m) {
				t.Errorf("%s: stub is not a pure delegation:\n%s", f.Path, m)
			}
		}
		if n != len(f.Stubs) {
			t.Errorf("%s: %d delegating stubs, want %d", f.Path, n, len(f.Stubs))
		}
	}
	for _, name := range declared {
		if name == "" {
			t.Error("region declares a function that is not a Body helper")
		}
	}
}

// TestFreeqLiteBlueprintsPlan builds the skeleton and, when the freeq-lite
// oracle is in the tree, checks it can connect (behaviour checks are expected
// to fail until converge has filled the bodies).
func TestFreeqLiteBlueprintsPlan(t *testing.T) {
	dir, _, p, in := grown(t)
	dropGenerators(t, dir)
	seed(t, dir, p)
	fill(t, dir, p, in)
	bin := filepath.Join(t.TempDir(), "freeqlite")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	oracle := filepath.Join("..", "..", "examples", "freeq-lite", "oracle")
	if _, err := os.Stat(filepath.Join(oracle, "main.go")); err != nil {
		t.Skip("freeq-lite oracle not in this tree yet (PR #11)")
	}
	t.Skip("oracle wiring lands once PR #11 merges; its flags are not on main")
}

func pin(id, typ, status string, scope ...string) records.Pin {
	return records.Pin{RecordID: id, GenHash: "g", Type: typ, Status: status, AffectedScope: scope}
}

func simple(t *testing.T, f File, p records.Pin) Input {
	return Input{Tier: "low", Contract: sedumContract(t, "go"), Records: []Rec{{Pin: p, Section: "s"}}, Skeleton: Skeleton{Files: []File{f}}}
}

func TestPlanValidation(t *testing.T) {
	okPin := pin("r1", "imprint", "open", "a.go")
	okFile := File{Path: "a.go", Section: "s", Decls: []Decl{{Kind: "var", Name: "X", Code: "var X = 1"}},
		Stubs: []Stub{{Name: "Do"}}}
	if _, err := Plan(simple(t, okFile, okPin)); err != nil {
		t.Fatalf("baseline must plan: %v", err)
	}
	with := func(f func(*File)) File {
		c := okFile
		c.Decls = append([]Decl(nil), okFile.Decls...)
		c.Stubs = append([]Stub(nil), okFile.Stubs...)
		f(&c)
		return c
	}
	cases := map[string]Input{
		"blueprint":          simple(t, okFile, pin("r1", "blueprint", "open", "a.go")),
		"brief":              simple(t, okFile, pin("r1", "brief", "open", "a.go")),
		"sealed status":      simple(t, okFile, pin("r1", "imprint", "sealed", "a.go")),
		"scope mismatch":     simple(t, okFile, pin("r1", "imprint", "open", "b.go")),
		"empty scope":        simple(t, okFile, pin("r1", "imprint", "open")),
		"no tier":            {Contract: sedumContract(t, "go"), Records: []Rec{{Pin: okPin, Section: "s"}}, Skeleton: Skeleton{Files: []File{okFile}}},
		"dup decl":           simple(t, with(func(f *File) { f.Decls = append(f.Decls, f.Decls[0]) }), okPin),
		"decl vs stub name":  simple(t, with(func(f *File) { f.Decls = append(f.Decls, Decl{Kind: "func", Name: "Do", Code: "func Do() {}"}) }), okPin),
		"receiver name only": simple(t, with(func(f *File) { f.Stubs[0].ReceiverName = "c" }), okPin),
		"receiver type only": simple(t, with(func(f *File) { f.Stubs[0].ReceiverType = "*T" }), okPin),
		"dup import":         simple(t, with(func(f *File) { f.Imports = []string{"net", "net"} }), okPin),
		"synth collision": {Tier: "low", Contract: sedumContract(t, "go"), Records: []Rec{{Pin: pin("r1", "imprint", "open", "a.go", "a_synth.go"), Section: "s"}},
			Skeleton: Skeleton{Files: []File{okFile, {Path: "a_synth.go", Section: "s"}}}},
		"file without owner": simple(t, with(func(f *File) { f.Section = "" }), okPin),
		"record and section": simple(t, with(func(f *File) { f.Record = "r1" }), okPin),
	}
	for name, in := range cases {
		if _, err := Plan(in); err == nil {
			t.Errorf("%s: Plan accepted it", name)
		}
	}
	// a record with no file in the skeleton
	in := simple(t, okFile, okPin)
	in.Records = append(in.Records, Rec{Pin: pin("r2", "imprint", "open", "b.go"), Section: "t"})
	if _, err := Plan(in); err == nil {
		t.Error("record with no file: Plan accepted it")
	}
	// implemented imprints are fine, and the recording is complete
	p, err := Plan(simple(t, okFile, pin("r1", "imprint", "implemented", "a.go")))
	if err != nil || !p.Recording.Complete {
		t.Errorf("implemented imprint: %v complete=%v", err, p.Recording.Complete)
	}
}
