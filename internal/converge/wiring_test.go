package converge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/records"
)

func sedumRegion(record, body string) string {
	sum := sha256.Sum256([]byte(body))
	return "// sedum:setFunction {\"v\":2,\"tier\":\"owned\",\"record\":\"" + record +
		"\",\"gen\":\"sha256:aaaa\",\"body\":\"sha256:" + hex.EncodeToString(sum[:]) + "\"}\n" +
		body + "// /sedum:setFunction\n"
}

func TestSedumRegionsAreUntouched(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	src := "package app\n\n" + sedumRegion("prov-sedum", "func A() {}\n")
	os.WriteFile(filepath.Join(root, "app.go"), []byte(src), 0o644)
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	got := read(t, root)
	if !strings.Contains(got, sedumRegion("prov-sedum", "func A() {}\n")) {
		t.Fatalf("sedum region changed:\n%s", got)
	}
}

// A synthesis region that sits inside a sedum region (e.g. someone moved the
// markers) is refused before anything runs.
func TestSynthesisInsideSedumRegionIsRefused(t *testing.T) {
	ran := 0
	c, root := setup(t, fakeGate{file: "app.go", want: "", kind: evidence.TestRun, ran: &ran})
	inner := "// " + beginTag + ` {"record":"prov-1","version":2,"gen":"g2","tier":"low","body":"` +
		HashBody("func F() {}\n") + "\"}\nfunc F() {}\n// " + endTag + " {\"record\":\"prov-1\"}\n"
	src := "package app\n\n" + sedumRegion("prov-sedum", inner)
	os.WriteFile(filepath.Join(root, "app.go"), []byte(src), 0o644)
	_, err := c.Converge(context.Background(), cand("func F() { return }"))
	if !errors.Is(err, ErrSedumRegion) {
		t.Fatalf("err = %v", err)
	}
	if ran != 0 {
		t.Fatal("gates ran")
	}
	if read(t, root) != src {
		t.Fatal("tree changed")
	}
}

func TestNewCandidateTakesPinFromRecords(t *testing.T) {
	s := fakeStore{"prov-1": {RecordID: "prov-1", Version: 7, GenHash: "sha256:g7"}}
	k, err := NewCandidate(s, "prov-1", "app.go", "low", "s1", "func F() {}")
	if err != nil || k.Version != 7 || k.GenHash != "sha256:g7" || k.RecordID != "prov-1" {
		t.Fatalf("%+v %v", k, err)
	}
	if _, err := NewCandidate(s, "prov-404", "app.go", "low", "", "x"); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

type fakeStore map[string]records.Pin

func (f fakeStore) Current(id string) (records.Pin, error) {
	p, ok := f[id]
	if !ok {
		return records.Pin{}, records.ErrNotFound
	}
	return p, nil
}
func (f fakeStore) Snapshot(string, int) ([]byte, error) { return nil, records.ErrNotFound }

// End to end over the real records fixture shipped with M1.
func TestNewCandidateOverRealFixture(t *testing.T) {
	dirs, _ := filepath.Glob("../records/testdata/*")
	if len(dirs) == 0 {
		t.Skip("no records fixtures")
	}
	for _, d := range dirs {
		s, err := records.OpenDir(d)
		if err != nil {
			continue
		}
		ids, _ := filepath.Glob(filepath.Join(d, "provenance", "*.yml"))
		for _, f := range ids {
			id := strings.TrimSuffix(filepath.Base(f), ".yml")
			k, err := NewCandidate(s, id, "app.go", "low", "", "func F() {}")
			if err != nil {
				t.Fatalf("%s/%s: %v", d, id, err)
			}
			if k.GenHash == "" || k.Version < 1 {
				t.Fatalf("%s/%s: empty pin %+v", d, id, k)
			}
			return
		}
	}
	t.Skip("no usable fixture record")
}
