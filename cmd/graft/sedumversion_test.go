package main

import (
	"bytes"
	"os"
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

// graft 0.2 with sedum 0.13 refuses with a clear version error, before the
// model is asked anything.
func TestDraftSkeletonRefusesSedum013(t *testing.T) {
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
	cfg, spec := filepath.Join(d, "graft.json"), filepath.Join(d, "spec.md")
	os.WriteFile(cfg, []byte(`{"module":"app","tier":"low","policy":{"low":["static_analysis"]},"gates":[],"conventions":"conv.md"}`), 0o644)
	os.WriteFile(filepath.Join(d, "conv.md"), []byte("Go.\n"), 0o644)
	os.WriteFile(spec, []byte(dsSpec), 0o644)
	var so, se bytes.Buffer
	code := run([]string{"draft", "--skeleton", "--config", cfg, "--spec", spec, "--out", filepath.Join(d, "p.json"), "--model", "false"}, &so, &se)
	if code != 2 || !strings.Contains(se.String(), "needs sedum >= 0.14.0, found 0.13.0") {
		t.Fatalf("exit %d, stderr %q", code, se.String())
	}
}
