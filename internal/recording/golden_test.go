package recording

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoGolden pins the exact bytes Plan produces for examples/freeq-lite: the
// recording and every region seed. The language-agnostic refactor (M10b) must
// keep them byte-identical. Regenerate with GRAFT_UPDATE_GOLDEN=1.
func TestGoGolden(t *testing.T) {
	in := freeqInput(t, t.TempDir())
	p, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	var rec bytes.Buffer
	if err := Write(&rec, p.Recording); err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{"recording.json": rec.Bytes()}
	for _, r := range p.Regions {
		got["seed-"+strings.ReplaceAll(r.Path, "/", "__")] = r.Seed
	}
	dir := filepath.Join("testdata", "golden")
	if os.Getenv("GRAFT_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for n, b := range got {
			if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for n, b := range got {
		want, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("%s: %v (GRAFT_UPDATE_GOLDEN=1 to create)", n, err)
		}
		if !bytes.Equal(want, b) {
			t.Errorf("%s differs from golden", n)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != len(got) {
		t.Errorf("golden has %d files, Plan produced %d", len(ents), len(got))
	}
}
