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
		{"registry.py", "channels.py", "from running import HANDLERS\nprint(len(HANDLERS))\n", "only running.py may read HANDLERS"},
		{"registry.py", "channels.py", "class X:\n    pass\n", "only running.py defines classes"},
		{"unused.py", "channels.py", "def channels_helper():\n    return 1\n", "channels_helper is never used"},
	}
	for _, c := range cases {
		files := map[string]string{"running.py": running, c.file: c.src}
		out, ok := gate(t, c.gate, files)
		if ok || !strings.Contains(out, c.want) {
			t.Errorf("%s on %q: ok=%v, want %q in:\n%s", c.gate, c.src, ok, c.want, out)
		}
	}
}
