package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The smoke suite must pass on the reference (v1 and v2), or it is wrong.
func TestSmokePassesOnReference(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	self, _ := filepath.Abs("smoke.go")
	out, err := exec.Command("go", "run", self, "-all", "-bin", bin, "-server-args", "-v2").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "12/12 checks passed") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestOnlyAndJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	self, _ := filepath.Abs("smoke.go")
	out, err := exec.Command("go", "run", self, "-only", "channels.go", "-json", "-bin", bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != `{"checks":2,"failed":[],"passed":2}` {
		t.Fatalf("%v %s", err, out)
	}
}

// The text summary is parsed by best-of-N's ranker; this pins its format.
func TestSummaryLineFormatIsStable(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	self, _ := filepath.Abs("smoke.go")
	out, _ := exec.Command("go", "run", self, "-only", "keepalive.go", "-bin", bin).Output()
	if !regexp.MustCompile(`(?m)^smoke: \d+/\d+ checks passed$`).Match(out) {
		t.Fatalf("summary line changed: %q", out)
	}
}

func TestBuildFailureReportsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "running.go"), []byte("package main\nfunc main( {}\n"), 0o644)
	self, _ := filepath.Abs("smoke.go")
	cmd := exec.Command("go", "run", self)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil || !strings.HasPrefix(string(out), "smoke: 0/1 checks passed") {
		t.Fatalf("%v %q", err, out)
	}
}
