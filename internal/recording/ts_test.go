package recording

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chad/graft/internal/render"
)

// TestTypeScriptPlanRendersAndLoads plans the freeq-lite-ts skeleton with the
// typescript generator's contract, grows it with real sedum, seeds the
// regions, and loads every module under Node's strip-types: the second
// language, with no TypeScript knowledge in graft.
func TestTypeScriptPlanRendersAndLoads(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("needs node")
	}
	if err := exec.Command(node, "--experimental-strip-types", "--no-warnings", "-e", "0").Run(); err != nil {
		t.Skip("needs a Node with --experimental-strip-types (22.6 or newer)")
	}
	bin, err := exec.LookPath("sedum")
	if err != nil {
		t.Skip("needs sedum")
	}
	b, err := os.ReadFile("../../examples/freeq-lite-ts/skeleton.json")
	if err != nil {
		t.Fatal(err)
	}
	sk, err := LoadSkeleton(b)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Generator != "typescript" {
		t.Fatalf("skeleton generator = %q", sk.Generator)
	}
	dir := t.TempDir()
	in := Input{Records: writeRecords(t, dir, sk), Skeleton: sk, Tier: "low", Contract: sedumContract(t, "typescript")}
	p, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Recording.Packages["typescript"]; !ok || len(p.Regions) != 7 {
		t.Fatalf("packages %v, %d regions", p.Recording.Packages, len(p.Regions))
	}
	for _, r := range p.Regions {
		if !strings.HasSuffix(r.Path, "_synth.ts") || !IsSynthPath(r.Path) || !bytes.Contains(r.Seed, []byte("// graft:synth:begin")) {
			t.Errorf("region %s: %s", r.Path, r.Seed)
		}
	}
	var buf bytes.Buffer
	if err := Write(&buf, p.Recording); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(dir, "recording.json")
	if err := os.WriteFile(rec, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	copyGenerators(t, dir)
	if err := render.Render(context.Background(), render.Exec(bin), dir, rec); err != nil {
		t.Fatal(err)
	}
	dropGenerators(t, dir)
	for _, r := range p.Regions {
		if err := os.WriteFile(filepath.Join(dir, r.Path), r.Seed, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type":"module"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var load strings.Builder
	load.WriteString(`await import("./running.ts");`)
	for _, name := range []string{"registration", "keepalive", "channels", "messages", "leaving", "everything_else"} {
		load.WriteString(`await import("./` + name + `.ts");`)
	}
	cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", "--input-type=module", "-e", load.String())
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the grown tree does not load: %v\n%s", err, out)
	}
	// An unconverged helper is a placeholder, and a stub reaches it.
	run := exec.Command(node, "--experimental-strip-types", "--no-warnings", "--input-type=module", "-e",
		`const m = await import("./registration.ts"); try { m.init(); } catch (e) { console.log(String(e)); }`)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "graft: not converged") {
		t.Fatalf("init did not reach its placeholder: %v\n%s", err, out)
	}
}
