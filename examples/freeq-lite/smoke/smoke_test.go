package main

import (
	"os/exec"
	"path/filepath"
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
	if err != nil || !strings.Contains(string(out), "11/11 checks passed") {
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
