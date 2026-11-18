package records

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureID = "prov-2026-aaaa0001"

func open(t *testing.T, name string) Store {
	t.Helper()
	s, err := OpenDir(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCurrentReadsFixtureRecord(t *testing.T) {
	p, err := open(t, "base").Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecordID != fixtureID || p.Version != 1 {
		t.Fatalf("pin = %+v", p)
	}
	if !strings.HasPrefix(p.GenHash, "sha256:") || len(p.GenHash) != len("sha256:")+64 {
		t.Fatalf("GenHash = %q", p.GenHash)
	}
}

func TestUnknownIDIsErrNotFound(t *testing.T) {
	s := open(t, "base")
	for _, id := range []string{"prov-nope", "", "..", "../../gen-moved/provenance/" + fixtureID} {
		if _, err := s.Current(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Current(%q) err = %v, want ErrNotFound", id, err)
		}
		if _, err := s.Snapshot(id, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("Snapshot(%q) err = %v, want ErrNotFound", id, err)
		}
	}
	// Resolves to a real snapshot dir in another fixture; only validID stops it.
	if _, err := s.Snapshot("../../../gen-moved/provenance/.versions/"+fixtureID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("traversing Snapshot err = %v, want ErrNotFound", err)
	}
	if _, err := s.Snapshot(fixtureID, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("Snapshot v9 err = %v, want ErrNotFound", err)
	}
	if _, err := OpenDir(filepath.Join("testdata", "missing")); err == nil {
		t.Error("OpenDir on a missing dir succeeded")
	}
}

func TestStaleWhenGenHashMoves(t *testing.T) {
	pin, err := open(t, "base").Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	moved := open(t, "gen-moved")
	cur, err := moved.Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 || cur.GenHash == pin.GenHash {
		t.Fatalf("fixture should be v2 with a new gen_hash: %+v vs %+v", cur, pin)
	}
	stale, err := Stale(moved, pin)
	if err != nil || !stale {
		t.Fatalf("Stale = %v, %v; want true", stale, err)
	}
}

func TestNotStaleOnStatusOnlyChange(t *testing.T) {
	pin, err := open(t, "base").Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	same := open(t, "status-only")
	cur, err := same.Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 || cur.GenHash != pin.GenHash {
		t.Fatalf("fixture should be v2 with the same gen_hash: %+v vs %+v", cur, pin)
	}
	stale, err := Stale(same, pin)
	if err != nil || stale {
		t.Fatalf("Stale = %v, %v; want false", stale, err)
	}
}

func TestStaleUnknownRecord(t *testing.T) {
	if _, err := Stale(open(t, "base"), Pin{RecordID: "prov-nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSnapshotIsImmutableBytes(t *testing.T) {
	vdir := filepath.Join("testdata", "base", "provenance", ".versions", fixtureID)
	matches, err := filepath.Glob(filepath.Join(vdir, "1-*.yml"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("fixture snapshot: %v %v", matches, err)
	}
	want, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	got, err := open(t, "base").Snapshot(fixtureID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Snapshot bytes differ from the .versions file")
	}
	// v1 stays byte-identical after later versions are added.
	later, err := open(t, "gen-moved").Snapshot(fixtureID, 1)
	if err != nil || !bytes.Equal(later, want) {
		t.Fatalf("v1 changed in later repo: %v", err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsTamperedVersion(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join("testdata", "base"), dir)
	matches, _ := filepath.Glob(filepath.Join(dir, "provenance", ".versions", fixtureID, "1-*.yml"))
	if len(matches) != 1 {
		t.Fatalf("fixture snapshot: %v", matches)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(b, []byte("Add the widget."), []byte("Add the evil widget."), 1)
	if bytes.Equal(b, tampered) {
		t.Fatal("tamper had no effect; fixture changed?")
	}
	if err := os.WriteFile(matches[0], tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Snapshot(fixtureID, 1); err == nil {
		t.Fatalf("tampered snapshot returned %d bytes with no error", len(got))
	}
}

func TestCurrentRejectsIDMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "provenance"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "base", "provenance", fixtureID+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	// File name says prov-other, the record inside says fixtureID.
	if err := os.WriteFile(filepath.Join(dir, "provenance", "prov-other.yml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.Current("prov-other"); err == nil {
		t.Fatalf("Current accepted a file whose inner id differs: %+v", p)
	}
}

func TestNoSpecsMeansEmptySpecHash(t *testing.T) {
	p, err := open(t, "base").Current(fixtureID)
	if err != nil || p.SpecHash != "" {
		t.Fatalf("%+v %v", p, err)
	}
}

// copyFixture copies testdata/<name> to a temp dir the test may modify.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", name)
	filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	return dst
}

func TestSpecHashTracksSpecContentNotGenHash(t *testing.T) {
	dir := copyFixture(t, "with-specs")
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(before.SpecHash, "sha256:") || len(before.SpecHash) != len("sha256:")+64 {
		t.Fatalf("SpecHash = %q", before.SpecHash)
	}
	// CRLF folds to LF (provenance's SpecHash): not a change.
	os.WriteFile(filepath.Join(dir, "specs", "widget.md"), []byte("# Widget\r\n\r\n- the widget renders\r\n"), 0o644)
	if p, _ := s.Current(fixtureID); p.SpecHash != before.SpecHash {
		t.Fatal("CRLF changed the spec hash")
	}
	os.WriteFile(filepath.Join(dir, "specs", "widget.md"), []byte("# Widget\n\n- the widget renders twice\n"), 0o644)
	after, err := s.Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SpecHash == before.SpecHash {
		t.Fatal("editing the spec did not move SpecHash")
	}
	if after.GenHash != before.GenHash {
		t.Fatal("editing a spec moved GenHash: specs verify, they do not drive generation")
	}
	if stale, _ := Stale(s, before); stale {
		t.Fatal("a spec-only edit made the record stale for generation")
	}
}

func TestMissingSpecIsAnErrorNotEmpty(t *testing.T) {
	dir := copyFixture(t, "with-specs")
	os.Remove(filepath.Join(dir, "specs", "widget.md"))
	s, _ := OpenDir(dir)
	if _, err := s.Current(fixtureID); !errors.Is(err, ErrSpecMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestSpecPathEscapeIsRefused(t *testing.T) {
	for _, bad := range []string{"../outside.md", "/etc/passwd", "specs/../../outside.md"} {
		dir := copyFixture(t, "with-specs")
		os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.md"), []byte("x"), 0o644)
		f := filepath.Join(dir, "provenance", fixtureID+".yml")
		b, _ := os.ReadFile(f)
		os.WriteFile(f, []byte(strings.Replace(string(b), "path: specs/widget.md", "path: "+bad, 1)), 0o644)
		s, _ := OpenDir(dir)
		if _, err := s.Current(fixtureID); !errors.Is(err, ErrSpecPath) {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

// Review on #10: Stale is gen_hash-only and must not need the spec files.
func TestStaleIgnoresMissingSpec(t *testing.T) {
	dir := copyFixture(t, "with-specs")
	s, _ := OpenDir(dir)
	p, err := s.Current(fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "specs", "widget.md"))
	stale, err := Stale(s, p)
	if err != nil || stale {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
}

func TestSymlinkedSpecLeavingRepoIsMissing(t *testing.T) {
	dir := copyFixture(t, "with-specs")
	outside := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(outside, []byte("secret"), 0o644)
	spec := filepath.Join(dir, "specs", "widget.md")
	os.Remove(spec)
	os.Symlink(outside, spec)
	s, _ := OpenDir(dir)
	if _, err := s.Current(fixtureID); !errors.Is(err, ErrSpecMissing) {
		t.Fatalf("err = %v", err)
	}
}

func twoSpecFixture(t *testing.T, first, second string) string {
	dir := copyFixture(t, "with-specs")
	os.WriteFile(filepath.Join(dir, "specs", "a.md"), []byte("alpha\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "specs", "b.md"), []byte("beta\n"), 0o644)
	f := filepath.Join(dir, "provenance", fixtureID+".yml")
	b, _ := os.ReadFile(f)
	two := "  - path: specs/" + first + "\n    type: integration\n  - path: specs/" + second + "\n    type: integration"
	os.WriteFile(f, []byte(strings.Replace(string(b), "  - path: specs/widget.md\n    type: integration", two, 1)), 0o644)
	return dir
}

// Review on #10: order matters (a swap moves the hash), and the encoding is
// pinned by a golden value, so neither sorting nor separator-less joining survives.
func TestSpecHashOrderAndGolden(t *testing.T) {
	ab, _ := OpenDir(twoSpecFixture(t, "a.md", "b.md"))
	ba, _ := OpenDir(twoSpecFixture(t, "b.md", "a.md"))
	p1, err1 := ab.Current(fixtureID)
	p2, err2 := ba.Current(fixtureID)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if p1.SpecHash == p2.SpecHash {
		t.Fatal("swapping spec order did not move the spec hash")
	}
	const golden = "sha256:d0c625e9be317d4826eac829a24f33a2be70a39b793555aff2073d35cdce55b7" // = sha256(JSON ["sha256:"+sha256("alpha\n"), "sha256:"+sha256("beta\n")]), reproduced independently
	if p1.SpecHash != golden {
		t.Fatalf("SpecHash = %s; if the encoding change is intended, update the golden value", p1.SpecHash)
	}
}
