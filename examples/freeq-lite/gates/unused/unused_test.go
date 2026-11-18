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
		"mutual recursion only": {map[string]string{
			"a.go": "package main\n\nfunc main() {}\n\nfunc ping(n int) { if n > 0 { pong(n - 1) } }\n\nfunc pong(n int) { if n > 0 { ping(n - 1) } }\n",
		}, false},
		"_ = f in init is not a use": {map[string]string{
			"a.go": "package main\n\nfunc main() {}\n\nfunc helper() {}\n\nfunc init() { _ = helper }\n",
		}, false},
		"var _ = func(){ f() }() runs f": {map[string]string{
			"a.go": "package main\n\nfunc main() {}\n\nfunc helper() {}\n\nvar _ = func() int { helper(); return 0 }()\n",
		}, true},
		"reached through a method": {map[string]string{
			"a.go": "package main\n\ntype T struct{}\n\nfunc (T) Do() { helper() }\n\nfunc main() { T{}.Do() }\n\nfunc helper() {}\n",
		}, true},
		"stored in a never-read global map": {map[string]string{
			"a.go": "package main\n\nfunc main() {}\n\nvar table = map[string]func(){\"x\": helper}\n\nfunc helper() {}\n",
		}, false},
		"stored in a global map that main reads": {map[string]string{
			"a.go": "package main\n\nfunc main() { table[\"x\"]() }\n\nvar table = map[string]func(){\"x\": helper}\n\nfunc helper() {}\n",
		}, true},
		"reachable only from an uncalled method": {map[string]string{
			"a.go": "package main\n\ntype T struct{}\n\nfunc (T) unused() { helper() }\n\nfunc main() {}\n\nfunc helper() {}\n",
		}, false},
		"through a String method": {map[string]string{
			"a.go": "package main\n\nimport \"fmt\"\n\ntype T struct{}\n\nfunc (T) String() string { return helper() }\n\nfunc main() { fmt.Println(T{}) }\n\nfunc helper() string { return \"\" }\n",
		}, true},
		"through sort.Interface": {map[string]string{
			"a.go": "package main\n\nimport \"sort\"\n\ntype byLen []string\n\nfunc (b byLen) Len() int { return len(b) }\nfunc (b byLen) Less(i, j int) bool { return less(b[i], b[j]) }\nfunc (b byLen) Swap(i, j int) { b[i], b[j] = b[j], b[i] }\n\nfunc main() { sort.Sort(byLen{\"a\"}) }\n\nfunc less(a, b string) bool { return len(a) < len(b) }\n",
		}, true},
		"through the package's own interface": {map[string]string{
			"a.go": "package main\n\ntype handler interface{ handle() }\n\ntype join struct{}\n\nfunc (join) handle() { helper() }\n\nfunc main() { var h handler = join{}; h.handle() }\n\nfunc helper() {}\n",
		}, true},
		"generic type method": {map[string]string{
			"a.go": "package main\n\ntype box[T any] struct{ v T }\n\nfunc (b box[T]) get() T { helper(); return b.v }\n\nfunc main() { _ = box[int]{}.get() }\n\nfunc helper() {}\n",
		}, true},
		"exported method (reflection)": {map[string]string{
			"a.go": "package main\n\nimport \"reflect\"\n\ntype T struct{}\n\nfunc (T) Run() { helper() }\n\nfunc main() { reflect.ValueOf(T{}).MethodByName(\"Run\").Call(nil) }\n\nfunc helper() {}\n",
		}, true},
		"var _ = f inside a function": {map[string]string{
			"a.go": "package main\n\nfunc main() { var _ = helper }\n\nfunc helper() {}\n",
		}, false},
		"promoted method": {map[string]string{
			"a.go": "package main\n\ntype base struct{}\n\nfunc (base) Hello() { helper() }\n\ntype outer struct{ base }\n\nfunc main() { outer{}.Hello() }\n\nfunc helper() {}\n",
		}, true},
		"goroutine-only": {map[string]string{
			"a.go": "package main\n\nfunc main() { go worker() }\n\nfunc worker() {}\n",
		}, true},
		"_ = (f)": {map[string]string{
			"a.go": "package main\n\nfunc main() { _ = (helper) }\n\nfunc helper() {}\n",
		}, false},
		"initializer calls something else": {map[string]string{
			"a.go": "package main\n\nimport \"fmt\"\n\nfunc main() {}\n\nfunc helper() {}\n\nvar _ = fmt.Sprint(helper)\n",
		}, false},
		"excluded by build tag": {map[string]string{
			"a.go":     "package main\n\nfunc main() {}\n",
			"other.go": "//go:build never\n\npackage main\n\nfunc ghost() {}\n",
		}, true},
		"reached through a handler map": {map[string]string{
			"a.go": "package main\n\nvar handlers = map[string]func(){}\n\nfunc main() { handlers[\"X\"]() }\n",
			"b.go": "package main\n\nfunc init() { handlers[\"X\"] = bHandle }\n\nfunc bHandle() { bUtil() }\n\nfunc bUtil() {}\n",
		}, true},
	}
	for name, c := range cases {
		out, ok := runUnused(t, c.files)
		if ok != c.pass {
			t.Errorf("%s: pass=%v want %v\n%s", name, ok, c.pass, out)
		}
	}
}
