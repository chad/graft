package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runUnused(t *testing.T, files map[string]string) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	for n, src := range files {
		os.WriteFile(filepath.Join(dir, n), []byte(src), 0o644)
	}
	self, _ := filepath.Abs("unused.go")
	cmd := exec.Command("go", "run", self)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func TestAllReferencedPasses(t *testing.T) {
	out, ok := runUnused(t, map[string]string{
		"a.go": "package main\n\nvar handlers = map[string]func(){}\n\nfunc main() { helper() }\n\nfunc helper() {}\n",
		"b.go": "package main\n\nfunc init() { handlers[\"X\"] = bHandle }\n\nfunc bHandle() { bUtil() }\n\nfunc bUtil() {}\n",
	})
	if !ok {
		t.Fatal(out)
	}
}

// The #14 probe's drop: a helper written for another file to call, never called.
func TestUncalledHelperFails(t *testing.T) {
	out, ok := runUnused(t, map[string]string{
		"a.go":      "package main\n\nfunc main() {}\n",
		"topics.go": "package main\n\n// channels.go must call it\nfunc topicsSendOnJoin() {}\n",
	})
	if ok || !strings.Contains(out, "func topicsSendOnJoin is never called") {
		t.Fatalf("ok=%v\n%s", ok, out)
	}
}
