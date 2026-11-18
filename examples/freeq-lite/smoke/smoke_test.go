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
	if err == nil || !strings.HasPrefix(string(out), "smoke: 0/1 checks passed") ||
		!strings.HasSuffix(strings.TrimRight(string(out), "\n"), "smoke: 0/1 checks passed") {
		t.Fatalf("summary must be first and last: %v %q", err, out)
	}
}

// -ext and -cmd: a non-Go tree is started by command, and which sections are
// generated is read with that extension (here only running.py exists).
func TestCmdAndExt(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("needs python3")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "running.py"), []byte(`import socket, sys, threading
addr = sys.argv[sys.argv.index("-addr") + 1]
host, port = addr.rsplit(":", 1)
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind((host, int(port))); s.listen()
def serve(c):
    while c.recv(4096):
        pass
while True:
    c, _ = s.accept()
    threading.Thread(target=serve, args=(c,), daemon=True).start()
`), 0o644)
	self, _ := filepath.Abs("smoke.go")
	cmd := exec.Command("go", "run", self, "-ext", ".py", "-cmd", `["`+py+`","running.py"]`)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "smoke: 1/1 checks passed") {
		t.Fatalf("%v\n%s", err, out)
	}
	// A server that never listens reports 0/Y, first and last.
	os.WriteFile(filepath.Join(dir, "running.py"), []byte("raise SystemExit(3)\n"), 0o644)
	cmd = exec.Command("go", "run", self, "-ext", ".py", "-cmd", `["`+py+`","running.py"]`)
	cmd.Dir = dir
	out, _ = cmd.Output()
	if !strings.HasPrefix(string(out), "smoke: 0/1 checks passed") || !strings.HasSuffix(strings.TrimRight(string(out), "\n"), "smoke: 0/1 checks passed") {
		t.Fatalf("%q", out)
	}
}
