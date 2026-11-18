// Package gates tests the freeq-lite-py gate scripts (they are Python; the
// test is Go so that `go test ./...` runs it).
package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const running = `HANDLERS = {}


class Client:
    def send(self, line):
        return line


def main(argv):
    c = Client()
    for k in HANDLERS:
        HANDLERS[k](None, c, None)
`

func gate(t *testing.T, script string, files map[string]string) (string, bool) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("needs python3")
	}
	dir := t.TempDir()
	for n, src := range files {
		os.WriteFile(filepath.Join(dir, n), []byte(src), 0o644)
	}
	self, _ := filepath.Abs(script)
	cmd := exec.Command(py, self)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func TestGates(t *testing.T) {
	good := map[string]string{
		"running.py": running,
		"channels.py": `from running import HANDLERS


def channels_join(server, client, msg):
    client.send("x")


HANDLERS["JOIN"] = channels_join
`,
	}
	for _, g := range []string{"check.py", "registry.py", "unused.py"} {
		if out, ok := gate(t, g, good); !ok {
			t.Fatalf("%s rejected a good tree:\n%s", g, out)
		}
	}
	cases := []struct{ gate, file, src, want string }{
		{"check.py", "channels.py", "from running import NOPE\n", "importing the modules failed"},
		{"check.py", "channels.py", "def (:\n", "channels.py"},
		{"registry.py", "channels.py", "from running import HANDLERS\n\n\ndef join(s, c, m):\n    pass\n\n\nHANDLERS['JOIN'] = join\n", "must be named channels_"},
		{"registry.py", "channels.py", "from running import HANDLERS\nprint(len(HANDLERS))\n", "only running.py may use HANDLERS"},
		{"registry.py", "channels.py", "class X:\n    pass\n", "only running.py defines classes"},
		{"unused.py", "channels.py", "def channels_helper():\n    return 1\n", "channels_helper is never used"},
		{"unused.py", "channels.py", "def channels_helper():\n    return 1\n\n\nNAME = 'channels_helper'\n", "channels_helper is never used"},
		{"unused.py", "channels.py", "def channels_loop(n):\n    return channels_loop(n - 1)\n", "channels_loop is never used"},
		{"unused.py", "channels.py", "def channels_helper():\n    return 1\n\n\n_ = channels_helper\n", "channels_helper is never used"},
		{"unused.py", "channels.py", "from running import HANDLERS\n\n\ndef channels_join(s, c, m):\n    def inner():\n        return 1\n    return 2\n\n\nHANDLERS['JOIN'] = channels_join\n", "inner is never used"},
		{"registry.py", "channels.py", "from running import HANDLERS as H\n", "must not be imported under another name"},
		{"registry.py", "channels.py", "import running\n\n\ndef channels_join(s, c, m):\n    pass\n\n\nrunning.HANDLERS['JOIN'] = channels_join\n", "not an attribute"},
	}
	for _, c := range cases {
		files := map[string]string{"running.py": running, c.file: c.src}
		out, ok := gate(t, c.gate, files)
		if ok || !strings.Contains(out, c.want) {
			t.Errorf("%s on %q: ok=%v, want %q in:\n%s", c.gate, c.src, ok, c.want, out)
		}
	}
}

func TestRegistryDuplicateCommand(t *testing.T) {
	reg := func(mod string) string {
		return "from running import HANDLERS\n\n\ndef " + mod + "_join(s, c, m):\n    pass\n\n\nHANDLERS['JOIN'] = " + mod + "_join\n"
	}
	out, ok := gate(t, "registry.py", map[string]string{"running.py": running, "channels.py": reg("channels"), "messages.py": reg("messages")})
	if ok || !strings.Contains(out, "JOIN is already registered by channels.py") {
		t.Fatalf("ok=%v\n%s", ok, out)
	}
}

// The committed run's tree (runs/sonnet-n1) passes every gate.
func TestRecordedRunPassesGates(t *testing.T) {
	files := map[string]string{}
	names, _ := filepath.Glob("../runs/sonnet-n1/tree/*.py")
	for _, n := range names {
		b, _ := os.ReadFile(n)
		files[filepath.Base(n)] = string(b)
	}
	if len(files) < 8 {
		t.Fatalf("recorded tree has %d files", len(files))
	}
	for _, g := range []string{"check.py", "registry.py", "unused.py"} {
		if out, ok := gate(t, g, files); !ok {
			t.Fatalf("%s rejected the recorded tree:\n%s", g, out)
		}
	}
}
