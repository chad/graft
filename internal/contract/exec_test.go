package contract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeSedum writes an executable that stands in for the sedum binary.
func fakeSedum(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	p := filepath.Join(t.TempDir(), "sedum")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecFailures(t *testing.T) {
	long := strings.Repeat("x", 1000)
	for name, c := range map[string]struct {
		script  string
		timeout time.Duration
		want    string // a substring of the error
		notWant string
	}{
		"hangs":              {script: "exec sleep 5", timeout: 100 * time.Millisecond, want: "no answer within"},
		"exits nonzero":      {script: "echo boom >&2; exit 3", want: "boom"},
		"not json":           {script: "echo 'this is not json'", want: "output: this is not json"},
		"long output is cut": {script: "printf '" + long + "'", want: "...", notWant: long},
	} {
		e := Exec{Bin: fakeSedum(t, c.script), Generators: "g", Package: "p", Timeout: c.timeout}
		_, err := e.Info()
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q lacks %q", name, err, c.want)
		}
		if c.notWant != "" && strings.Contains(err.Error(), c.notWant) {
			t.Errorf("%s: error repeats the whole output", name)
		}
		if _, err := e.Render(Input{}); err == nil {
			t.Errorf("%s: Render: no error", name)
		}
	}
}
