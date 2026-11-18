package model

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandPipesPromptThrough(t *testing.T) {
	out, err := Command{Argv: []string{"cat"}}.Complete(context.Background(), "hello model")
	if err != nil || out != "hello model" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestCommandFailures(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]Command{
		"no argv":  {},
		"non-zero": {Argv: []string{"sh", "-c", "echo boom >&2; exit 3"}},
		"empty":    {Argv: []string{"true"}},
		"timeout":  {Argv: []string{"sleep", "5"}, Timeout: 100 * time.Millisecond},
	} {
		if _, err := c.Complete(ctx, "x"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	_, err := Command{Argv: []string{"sh", "-c", "echo boom >&2; exit 3"}}.Complete(ctx, "x")
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("stderr not reported: %v", err)
	}
}

func TestExtractCode(t *testing.T) {
	resp := "Here you go:\n```go\npackage a\n```\nand the final version:\n```go\npackage b\n\nfunc F() {}\n```\nDone."
	if got := ExtractCode(resp, "go"); got != "package b\n\nfunc F() {}\n" {
		t.Fatalf("%q", got)
	}
	if got := ExtractCode("package c\n", "go"); got != "package c\n" {
		t.Fatalf("%q", got)
	}
	if got := ExtractCode("```python\nx=1\n```\n```\npackage d\n```", "go"); got != "package d\n" {
		t.Fatalf("%q", got)
	}
}

// The model command runs in an empty directory: it cannot read the repo.
func TestCommandRunsInAnEmptyDir(t *testing.T) {
	out, err := Command{Argv: []string{"sh", "-c", "ls -A | wc -l"}}.Complete(context.Background(), "")
	if err != nil || strings.TrimSpace(out) != "0" {
		t.Fatalf("%q %v", out, err)
	}
}
