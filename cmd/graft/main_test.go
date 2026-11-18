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

func TestDraftHidesStatementsOfUnvalidatedDrafts(t *testing.T) {
	spec := t.TempDir() + "/spec.md"
	os.WriteFile(spec, []byte("# Books\n\n- Users can add a book\n- Two books must never share an ISBN; a repeat is a conflict\n"), 0o644)
	var out, errb bytes.Buffer
	if code := run([]string{"draft", spec}, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "no confident proposal") || strings.Contains(out.String(), "Users can add a book") {
		t.Fatalf("an unvalidated draft's statements were shown:\n%s", out.String())
	}
}

func TestRunLanguageProfileRefusals(t *testing.T) {
	dir := t.TempDir()
	spec := dir + "/spec.md"
	os.WriteFile(spec, []byte("# T\n\n## Core\n\n- It must run\n"), 0o644)
	os.WriteFile(dir+"/conv.md", []byte("conventions\n"), 0o644)
	write := func(lang string) string {
		p := dir + "/graft.json"
		os.WriteFile(p, []byte(`{"tier":"low","conventions":"conv.md","model":{"argv":["true"]},"policy":{"low":["static_analysis"]},
"gates":[{"label":"g","argv":["true"],"kind":"static_analysis"}],"language":`+lang+`}`), 0o644)
		return p
	}
	cases := []struct{ lang, flag, want string }{
		{`{"ext":".py"}`, "", "name, ext, fence, comment and run are required"},
		{`{"name":"python","ext":".py","fence":"python","comment":"#","run":["python3","main.py"],"Ext":".x"}`, "", "unknown key"},
		{`{"name":"python","ext":".py","fence":"python","comment":"#","run":["python3","main.py"]}`, "--render", "--render supports only Go"},
	}
	for _, c := range cases {
		args := []string{"run", "--config", write(c.lang), "--spec", spec, "--repo", dir + "/r", "--root", dir + "/a", "--accept"}
		if c.flag != "" {
			args = append(args, c.flag)
		}
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), c.want) {
			t.Errorf("%s %s: exit %d, stderr %q (want %q)", c.lang, c.flag, code, errb.String(), c.want)
		}
	}
}
