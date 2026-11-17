package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/chad/graft/internal/version"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("got %q, want %q", got, version.Version)
	}
}

func TestNoArgsIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage: graft") {
		t.Fatalf("no usage on stderr: %q", errb.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestDraftPrintsProposalsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	spec := dir + "/spec.md"
	if err := os.WriteFile(spec, []byte("# App\n\n## Tasks\n\n- Users can create a task with a title\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"draft", spec}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "App › Tasks — new") || !strings.Contains(out.String(), "receipt: true") {
		t.Fatalf("output:\n%s", out.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("draft wrote files: %v", entries)
	}
}
