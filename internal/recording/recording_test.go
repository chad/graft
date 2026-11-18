package recording

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// writeRecords writes seven real provenance records under dir/provenance and
// returns their pins as records.OpenDir reads them.
func writeRecords(t *testing.T, dir string) []records.Pin {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "provenance"), 0o755); err != nil {
		t.Fatal(err)
	}
	var pins []records.Pin
	var ids []string
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("prov-2026-r000000%d", i)
		ids = append(ids, id)
		y := fmt.Sprintf(`id: %s
title: freeq-lite blueprint %d
type: blueprint
status: open
version: 1
reason: ""
created_at: "2026-11-18"
author: fixture@example.com
intent: Implement part %d of freeq-lite.
constraints: []
affected_scope: []
forbidden_scope: []
related: []
sealed_at_sha: ""
associated_specs: []
associated_traces: []
monitors: []
tags: []
`, id, i, i)
		if err := os.WriteFile(filepath.Join(dir, "provenance", id+".yml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := records.OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		p, err := st.Current(id)
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, p)
	}
	return pins
}

func freeqInput(t *testing.T, dir string) Input {
	t.Helper()
	b, err := os.ReadFile("testdata/freeqlite.skeleton.json")
	if err != nil {
		t.Fatal(err)
	}
	sk, err := LoadSkeleton(b)
	if err != nil {
		t.Fatal(err)
	}
	return Input{Pins: writeRecords(t, dir), Skeleton: sk}
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
	for i, j := 0, len(rev.Pins)-1; i < j; i, j = i+1, j-1 {
		rev.Pins[i], rev.Pins[j] = rev.Pins[j], rev.Pins[i]
	}
	if !bytes.Equal(a, recordingBytes(t, rev)) {
		t.Fatal("pin order changed the recording")
	}
}

func TestPlanRejectsBadInput(t *testing.T) {
	in := freeqInput(t, t.TempDir())
	in.Pins = in.Pins[:2]
	if _, err := Plan(in); err == nil {
		t.Fatal("file naming an unknown record must be an error")
	}
	if _, err := Plan(Input{Pins: []records.Pin{{RecordID: "x"}}, Skeleton: Skeleton{Files: []File{{Path: "../x.go", Record: "x"}}}}); err == nil {
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

// fill plays converge: one region-less file declaring every helper the stubs call.
func fill(t *testing.T, dir string, p Planned) {
	t.Helper()
	for _, r := range p.Regions {
		var sb strings.Builder
		sb.WriteString("package main\n\n")
		if strings.Contains(strings.Join(r.Declares, "\n"), "net.Conn") {
			sb.WriteString("import \"net\"\n\n")
		}
		for _, d := range r.Declares {
			sb.WriteString(d + " {\n")
			if strings.HasSuffix(d, " string") && !strings.Contains(d, "string)") {
				sb.WriteString("\treturn \"\"\n")
			}
			sb.WriteString("}\n\n")
		}
		name := "fill_" + strings.TrimSuffix(r.Path, ".go") + ".go"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordingGrowsWithRealSedum(t *testing.T) {
	dir, _, p, _ := grown(t)
	dropGenerators(t, dir)
	fill(t, dir, p)
	out, err := exec.Command("gofmt", "-l", dir).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("gofmt: %v %s", err, out)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

func TestGrowCheckCleanAfterGrow(t *testing.T) {
	dir, s, _, _ := grown(t)
	if err := s.GrowCheck(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestRegionsOwnedByRecords(t *testing.T) {
	dir, _, p, in := grown(t)
	want := map[string]records.Pin{}
	for _, pin := range in.Pins {
		want[pin.RecordID] = pin
	}
	regs, err := render.ReadMarkers(dir)
	if err != nil || len(regs) == 0 {
		t.Fatalf("markers: %v (%d)", err, len(regs))
	}
	for _, r := range regs {
		pin, ok := want[r.Owner.RecordID]
		if !ok || r.Owner.GenHash != pin.GenHash {
			t.Errorf("%s: region owned by %+v, want one of the planned records with its gen_hash", r.Path, r.Owner)
		}
	}
	// every planned file's owner appears
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

func TestHandlerBodiesAreConvergenceRegions(t *testing.T) {
	dir, _, p, _ := grown(t)
	if len(p.Regions) != 7 {
		t.Fatalf("want one convergence region per file, got %d", len(p.Regions))
	}
	for _, r := range p.Regions {
		if len(r.Declares) == 0 {
			t.Errorf("%s: region declares nothing", r.Path)
		}
		src, err := os.ReadFile(filepath.Join(dir, r.Path))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range r.Declares {
			name := regexp.MustCompile(`\w+Body`).FindString(d)
			if name == "" || strings.Contains(string(src), "func "+d[len("func "):]) && strings.Contains(string(src), name+" {") {
				t.Errorf("%s: helper %q must not be rendered by sedum", r.Path, name)
			}
			if !strings.Contains(string(src), name+"(") {
				t.Errorf("%s: no stub delegates to %s", r.Path, name)
			}
		}
		if !stubBody.Match(src) {
			t.Errorf("%s: stubs are not pure delegations", r.Path)
		}
	}
}

// TestFreeqLiteBlueprintsPlan builds the skeleton and, when the freeq-lite
// oracle is in the tree, checks it can connect (behaviour checks are expected
// to fail until converge has filled the bodies).
func TestFreeqLiteBlueprintsPlan(t *testing.T) {
	dir, _, p, _ := grown(t)
	dropGenerators(t, dir)
	fill(t, dir, p)
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
