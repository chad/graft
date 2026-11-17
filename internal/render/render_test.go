package render

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSedum struct{ executed int }

func (f *fakeSedum) Version(context.Context) (string, error) { return "fake", nil }
func (f *fakeSedum) GrowExecute(context.Context, string, string) error {
	f.executed++
	return nil
}
func (f *fakeSedum) GrowCheck(context.Context, string) error { return nil }

// region builds a file body in real sedum v2 marker syntax whose body hash is
// computed from body (or from hashOf when non-empty, to fake drift).
func region(tier, record, action, body, hashOf string) string {
	if hashOf == "" {
		hashOf = body
	}
	return "// sedum:" + action + ` {"v":2,"tier":"` + tier + `","record":"` + record +
		`","gen":"sha256:aaaa","id":{"name":"x"},"kwargs":"sha256:bbbb","body":"` + hashBody(hashOf) + `"}` + "\n" +
		body + "// /sedum:" + action + "\n"
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadMarkersParsesOwnerAndHashes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "b/two.go", "package b\n"+region("owned", "rec-2", "setFunction", "func B() {}\n", ""))
	write(t, root, "a/one.go", "package a\n"+region("owned", "rec-1", "setFunction", "func A() {}\n", ""))
	rs, err := ReadMarkers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d regions, want 2", len(rs))
	}
	if rs[0].Path != "a/one.go" || rs[1].Path != "b/two.go" {
		t.Fatalf("not sorted by path: %+v", rs)
	}
	if rs[0].Owner.RecordID != "rec-1" || rs[0].Owner.GenHash != "sha256:aaaa" {
		t.Errorf("owner = %+v", rs[0].Owner)
	}
	if rs[0].BodyHash != hashBody("func A() {}\n") || rs[0].Actual != rs[0].BodyHash {
		t.Errorf("hashes = %q %q", rs[0].BodyHash, rs[0].Actual)
	}
	if rs[0].Seeded || rs[0].Edited() {
		t.Errorf("unexpected seeded/edited: %+v", rs[0])
	}
}

func TestEditedDetectsHandEdit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", region("owned", "rec-1", "setFunction", "func A() { hacked() }\n", "func A() {}\n"))
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
	if !rs[0].Edited() {
		t.Fatal("hand edit not detected")
	}
}

func TestRenderRefusesOverHandEdit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", region("owned", "rec-1", "setFunction", "edited\n", "original\n"))
	f := &fakeSedum{}
	err := Render(context.Background(), f, root, "rec.json")
	if !errors.Is(err, ErrHandEdited) {
		t.Fatalf("err = %v, want ErrHandEdited", err)
	}
	if !strings.Contains(err.Error(), "a.go") {
		t.Errorf("error does not name the region: %v", err)
	}
	if f.executed != 0 {
		t.Fatal("GrowExecute was called")
	}
}

func TestRenderIgnoresSeededEdits(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", region("seeded", "rec-1", "setFunction", "human finished this\n", "stub\n"))
	f := &fakeSedum{}
	if err := Render(context.Background(), f, root, "rec.json"); err != nil {
		t.Fatal(err)
	}
	if f.executed != 1 {
		t.Fatalf("executed = %d", f.executed)
	}
}

func TestRenderCallsGrowExecuteWhenClean(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", region("owned", "rec-1", "setFunction", "clean\n", ""))
	f := &fakeSedum{}
	if err := Render(context.Background(), f, root, "rec.json"); err != nil {
		t.Fatal(err)
	}
	if f.executed != 1 {
		t.Fatalf("executed = %d", f.executed)
	}
}

func TestExecAgainstRealSedum(t *testing.T) {
	bin, err := exec.LookPath("sedum")
	if err != nil {
		t.Skip("sedum not on PATH")
	}
	s := Exec(bin)
	v, err := s.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(v) == "" {
		t.Fatal("empty version")
	}
	// A bogus recording must surface as an error, not a panic or silent success.
	if err := s.GrowExecute(context.Background(), t.TempDir(), "does-not-exist.json"); err == nil {
		t.Fatal("expected error for missing recording")
	}
}
