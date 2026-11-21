package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every command that drives sedum builds it with render.Open, which refuses a
// sedum older than render.MinSedum. A bare render.Exec in a command would skip
// the check, so none may appear outside tests.
func TestNoCommandBuildsAnUncheckedSedum(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "render.Exec(") {
			t.Errorf("%s calls render.Exec; use render.Open so a sedum older than render.MinSedum is refused", f)
		}
	}
}

// oldSedum puts a sedum 0.13.0 on PATH, and a generators directory in
// SEDUM_GENERATORS so the version check is what refuses.
func oldSedum(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sedum"), []byte("#!/bin/sh\necho 0.13.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gens := filepath.Join(d, "generators")
	if err := os.Mkdir(gens, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SEDUM_GENERATORS", gens)
	return d
}

const refusal = "needs sedum >= 0.14.0, found 0.13.0"

// graft 0.2 with sedum 0.13 refuses with a clear version error, before the
// model is asked anything.
func TestDraftSkeletonRefusesSedum013(t *testing.T) {
	d := oldSedum(t)
	cfg, spec := filepath.Join(d, "graft.json"), filepath.Join(d, "spec.md")
	os.WriteFile(cfg, []byte(`{"module":"app","tier":"low","policy":{"low":["static_analysis"]},"gates":[],"conventions":"conv.md"}`), 0o644)
	os.WriteFile(filepath.Join(d, "conv.md"), []byte("Go.\n"), 0o644)
	os.WriteFile(spec, []byte(dsSpec), 0o644)
	var so, se bytes.Buffer
	code := run([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", filepath.Join(d, "p.json"), "--model", "false"}, &so, &se)
	if code != 2 || !strings.Contains(se.String(), refusal) {
		t.Fatalf("exit %d, stderr %q", code, se.String())
	}
}

// `run --render --accept` with sedum 0.13 exits 2 and writes nothing: the
// version is checked before any record is accepted.
func TestRunRenderRefusesSedum013AndWritesNothing(t *testing.T) {
	oldSedum(t)
	ex := filepath.Join("..", "..", "examples", "freeq-lite")
	repo, root := filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "app")
	var so, se bytes.Buffer
	code := run([]string{"run", "--render", "--accept", "--config", filepath.Join(ex, "graft.json"), "--spec", filepath.Join(ex, "spec", "freeq-lite.md"),
		"--skeleton", filepath.Join(ex, "skeleton.json"), "--repo", repo, "--root", root}, &so, &se)
	if code != 2 || !strings.Contains(se.String(), refusal) {
		t.Fatalf("exit %d, stderr %q", code, se.String())
	}
	for _, p := range []string{repo, root} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("a refused run left %s behind", p)
		}
	}
}

// `bench run --arms graft-render` refuses the old sedum the same way.
func TestBenchGraftRenderRefusesSedum013(t *testing.T) {
	oldSedum(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go (the bench builds the oracle first)")
	}
	var so, se bytes.Buffer
	code := run([]string{"bench", "run", "--case", filepath.Join("..", "..", "examples", "freeq-lite", "case.json"),
		"--arms", "graft-render", "--resolution", "smoke", "--results", t.TempDir()}, &so, &se)
	if code != 2 || !strings.Contains(se.String(), refusal) {
		t.Fatalf("exit %d, stderr %q", code, se.String())
	}
}
