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

// Review on #18: precise resolution.
func TestReviewCases(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		pass  bool
	}{
		"same-named method is not a use": {map[string]string{
			"a.go": "package main\n\ntype T struct{}\n\nfunc (T) helper() {}\n\nfunc main() { T{}.helper() }\n\nfunc helper() {}\n",
		}, false},
		"var _ = f is not a use": {map[string]string{
			"a.go": "package main\n\nfunc main() {}\n\nfunc helper() {}\n\nvar _ = helper\n",
		}, false},
		"test-only caller counts": {map[string]string{
			"a.go":      "package main\n\nfunc main() {}\n\nfunc helper() int { return 1 }\n",
			"a_test.go": "package main\n\nimport \"testing\"\n\nfunc TestH(t *testing.T) { _ = helper() }\n",
		}, true},
		"exported but used": {map[string]string{
			"a.go": "package main\n\nfunc main() { Helper() }\n\nfunc Helper() {}\n",
		}, true},
		"self-recursion only": {map[string]string{
			"a.go": "package main\n\nfunc main() {}\n\nfunc loop(n int) { if n > 0 { loop(n - 1) } }\n",
		}, false},
	}
	for name, c := range cases {
		out, ok := runUnused(t, c.files)
		if ok != c.pass {
			t.Errorf("%s: pass=%v want %v\n%s", name, ok, c.pass, out)
		}
	}
}
