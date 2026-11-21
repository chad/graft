package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGate(t *testing.T, files map[string]string) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	for n, src := range files {
		os.WriteFile(filepath.Join(dir, n), []byte(src), 0o644)
	}
	self, _ := filepath.Abs("registry.go")
	cmd := exec.Command("go", "run", self)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

const core = "package main\n\nvar handlers = map[string]func(){}\n"

func TestOwnCommandsPass(t *testing.T) {
	out, ok := runGate(t, map[string]string{
		"running.go":  core,
		"channels.go": "package main\n\nfunc init() { handlers[\"JOIN\"] = func() {}; handlers[\"PART\"] = func() {} }\n",
		"topics.go":   "package main\n\nfunc init() { handlers[\"TOPIC\"] = func() {} }\n",
	})
	if !ok {
		t.Fatal(out)
	}
}

// The probe from the #14 review: Topics wrapping JOIN.
func TestWrappingAnotherFilesCommandFails(t *testing.T) {
	out, ok := runGate(t, map[string]string{
		"running.go":  core,
		"channels.go": "package main\n\nfunc init() { handlers[\"JOIN\"] = func() {} }\n",
		"topics.go":   "package main\n\nfunc init() { prev := handlers[\"JOIN\"]; handlers[\"JOIN\"] = func() { prev() } }\n",
	})
	if ok || !strings.Contains(out, `handlers["JOIN"] is already registered by channels.go`) {
		t.Fatalf("ok=%v\n%s", ok, out)
	}
}

func TestNonLiteralKeyAndDeleteFail(t *testing.T) {
	for name, src := range map[string]string{
		"non-literal": "package main\n\nfunc init() { k := \"JOIN\"; handlers[k] = func() {} }\n",
		"delete":      "package main\n\nfunc init() { delete(handlers, \"JOIN\") }\n",
	} {
		if out, ok := runGate(t, map[string]string{"running.go": core, "x.go": src}); ok {
			t.Errorf("%s passed:\n%s", name, out)
		}
	}
}

// Review on #14: the gate must not be bypassed by indirect forms.
func TestIndirectRegistryUseFails(t *testing.T) {
	for name, src := range map[string]string{
		"alias":      "package main\n\nfunc init() { h := handlers; h[\"JOIN\"] = func() {} }\n",
		"paren":      "package main\n\nfunc init() { (handlers)[\"JOIN\"] = func() {} }\n",
		"passed":     "package main\n\nimport \"fmt\"\n\nfunc init() { fmt.Println(handlers) }\n",
		"reassigned": "package main\n\nfunc init() { handlers = map[string]func(){} }\n",
		"read in fn": "package main\n\nfunc topicsWrap() { _ = handlers[\"JOIN\"] }\n",
	} {
		out, ok := runGate(t, map[string]string{"running.go": core, "topics.go": src})
		if ok {
			t.Errorf("%s passed:\n%s", name, out)
		}
	}
	// The declaring file may read the registry (its dispatcher does).
	disp := core + "\nfunc dispatch(c string) { if h, ok := handlers[c]; ok { h() } }\n"
	if out, ok := runGate(t, map[string]string{"running.go": disp}); !ok {
		t.Fatalf("dispatcher refused:\n%s", out)
	}
}

// The declaring file's _synth.go (render mode) is part of the declaring file.
func TestDeclarerSynthMayReadRegistry(t *testing.T) {
	disp := "package main\n\nfunc runningServe(c string) { if h, ok := handlers[c]; ok { h() } }\n"
	if out, ok := runGate(t, map[string]string{"running.go": core, "running_synth.go": disp}); !ok {
		t.Fatal(out)
	}
	if out, ok := runGate(t, map[string]string{"running.go": core, "topics_synth.go": disp}); ok {
		t.Fatal("other synth file passed:\n" + out)
	}
}

// Review on #19: the declarer's _synth.go may read the registry, not own it.
func TestDeclarerSynthReadOnly(t *testing.T) {
	for name, src := range map[string]string{
		"alias":    "package main\n\nfunc f() { m := handlers; old := m[\"JOIN\"]; m[\"JOIN\"] = func() { old() } }\n",
		"clear":    "package main\n\nfunc f() { clear(handlers) }\n",
		"write":    "package main\n\nfunc f() { handlers[\"JOIN\"] = func() {} }\n",
		"paren":    "package main\n\nfunc f() { (handlers)[\"JOIN\"] = func() {} }\n",
		"delete":   "package main\n\nfunc f() { delete(handlers, \"JOIN\") }\n",
		"passed":   "package main\n\nfunc g(map[string]func()) {}\nfunc f() { g(handlers) }\n",
		"reassign": "package main\n\nfunc f() { handlers = nil }\n",
		"addr":     "package main\n\nfunc f() { p := &handlers; _ = p }\n",
	} {
		files := map[string]string{"running.go": core, "channels.go": "package main\n\nfunc init() { handlers[\"JOIN\"] = func() {} }\n", "running_synth.go": src}
		if out, ok := runGate(t, files); ok {
			t.Errorf("%s passed:\n%s", name, out)
		}
	}
	ok := "package main\n\nfunc f(c string) int { n := 0; for k := range handlers { _ = k; n++ }; if h, found := handlers[c]; found { h() }; return n }\n"
	if out, pass := runGate(t, map[string]string{"running.go": core, "running_synth.go": ok}); !pass {
		t.Fatalf("read-only use refused:\n%s", out)
	}
	// len(handlers) is a read (review on #19, a false positive before).
	withLen := "package main\n\nfunc f() int { return len(handlers) }\n"
	if out, pass := runGate(t, map[string]string{"running.go": core, "running_synth.go": withLen}); !pass {
		t.Fatalf("len(handlers) refused:\n%s", out)
	}
	// A shadowing len is not the builtin.
	shadow := "package main\n\nfunc len(m map[string]func()) int { m[\"X\"] = nil; return 0 }\nfunc f() int { return len(handlers) }\n"
	if out, pass := runGate(t, map[string]string{"running.go": core, "running_synth.go": shadow}); pass {
		t.Fatalf("a shadowing len passed:\n%s", out)
	}
}
