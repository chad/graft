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
	"github.com/chad/graft/internal/render"
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
	s := fakeStore{"prov-1": {RecordID: "prov-1", Version: 7, GenHash: "sha256:g7", SpecHash: "sha256:s7"}}
	k, err := NewCandidate(s, "prov-1", "app.go", "low", "func F() {}")
	if err != nil || k.Version != 7 || k.GenHash != "sha256:g7" || k.SpecHash != "sha256:s7" || k.RecordID != "prov-1" {
		t.Fatalf("%+v %v", k, err)
	}
	if _, err := NewCandidate(s, "prov-404", "app.go", "low", "x"); !errors.Is(err, records.ErrNotFound) {
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

// End to end over M1's real fixture: the candidate pin is exactly the store's.
func TestNewCandidateOverRealFixture(t *testing.T) {
	s, err := records.OpenDir("../records/testdata/base")
	if err != nil {
		t.Fatal(err)
	}
	const id = "prov-2026-aaaa0001"
	want, err := s.Current(id)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewCandidate(s, id, "app.go", "low", "func F() {}")
	if err != nil {
		t.Fatal(err)
	}
	if k.RecordID != id || k.Version != want.Version || k.GenHash != want.GenHash || k.SpecHash != want.SpecHash || !strings.HasPrefix(k.GenHash, "sha256:") {
		t.Fatalf("candidate %+v, store %+v", k, want)
	}
}

func TestSedumRegionBytesIncludingMarkersAndCRLFArePreserved(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	sr := strings.ReplaceAll(sedumRegion("prov-sedum", "func A() {}\n"), "\n", "\r\n")
	src := "package app\n\n" + sr
	os.WriteFile(filepath.Join(root, "app.go"), []byte(src), 0o644)
	if d, err := c.Converge(context.Background(), cand("func F() {}")); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if !strings.Contains(read(t, root), sr) {
		t.Fatal("sedum region bytes changed")
	}
}

func TestLiteralPinIsNotTrusted(t *testing.T) {
	ran := 0
	c, root := setup(t, fakeGate{file: "app.go", want: "", kind: evidence.TestRun, ran: &ran})
	k := cand("func F() {}")
	k.Version, k.GenHash = 99, "forged"
	if _, err := c.Converge(context.Background(), k); !errors.Is(err, ErrStalePin) {
		t.Fatalf("err = %v", err)
	}
	if ran != 0 || read(t, root) != "package app\n" {
		t.Fatal("ran or wrote with a forged pin")
	}
}

func TestConvergerRequiresRecords(t *testing.T) {
	c, _ := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	c.Records = nil
	if _, err := c.Converge(context.Background(), cand("func F() {}")); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("err = %v", err)
	}
}

// movingStore bumps the record when its gate runs: the record moved mid-run.
type movingGate struct{ s fakeStore }

func (g movingGate) Name() string        { return "mover" }
func (g movingGate) Kind() evidence.Kind { return evidence.TestRun }
func (g movingGate) Run(context.Context, string) (evidence.Verdict, string, error) {
	g.s["prov-1"] = records.Pin{RecordID: "prov-1", Version: 3, GenHash: "g3", SpecHash: "s1"}
	return evidence.Pass, "", nil
}

func TestRecordMovingDuringGatesIsNotApplied(t *testing.T) {
	c, root := setup(t)
	c.Gates = []Gate{movingGate{c.Records.(fakeStore)}}
	d, err := c.Converge(context.Background(), cand("func F() {}"))
	if !errors.Is(err, ErrStalePin) || d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if read(t, root) != "package app\n" {
		t.Fatal("applied against a moved record")
	}
}

// Only the overlap check catches this: the candidate body equals the existing
// one, so the rendered file is byte-identical and every sedum region with it.
func TestOverlapRefusedEvenWhenNothingWouldChange(t *testing.T) {
	c, root := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	inner := "// " + beginTag + ` {"record":"prov-1","version":2,"gen":"g2","tier":"low","body":"` +
		HashBody("func F() {}\n") + "\"}\nfunc F() {}\n// " + endTag + " {\"record\":\"prov-1\"}\n"
	src := "package app\n\n" + sedumRegion("prov-sedum", inner)
	os.WriteFile(filepath.Join(root, "app.go"), []byte(src), 0o644)
	if _, err := c.Converge(context.Background(), cand("func F() {}")); !errors.Is(err, ErrSedumRegion) {
		t.Fatalf("err = %v", err)
	}
}

func TestGuardSedumUnit(t *testing.T) {
	k := cand("func F() {}")
	before := []byte("package app\n\n" + sedumRegion("s1", "func A() {}\n"))
	cases := map[string]string{
		"region added":   string(before) + sedumRegion("s2", "func B() {}\n"),
		"region removed": "package app\n",
		"marker changed": strings.Replace(string(before), `"gen":"sha256:aaaa"`, `"gen":"sha256:bbbb"`, 1),
		"body changed":   strings.Replace(string(before), "func A() {}", "func A() { _ = 1 }", 1),
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			if err := guardSedum(k, before, []byte(next)); !errors.Is(err, ErrSedumRegion) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if err := guardSedum(k, before, append(append([]byte(nil), before...), "func F() {}\n"...)); err != nil {
		t.Fatalf("an unrelated append was refused: %v", err)
	}
}

func TestSedumMarkerInBodyIsRefused(t *testing.T) {
	c, _ := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	for _, body := range []string{"// sedum:setFunction {}\nx\n// /sedum:setFunction", "  # /sedum:x"} {
		if _, err := c.Converge(context.Background(), cand(body)); !errors.Is(err, ErrMarkerInBody) {
			t.Errorf("%q: err = %v", body, err)
		}
	}
	// Prose that merely mentions sedum is fine.
	if d, err := c.Converge(context.Background(), cand(`var s = "see sedum: docs"`)); err != nil || !d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestOverlapsBoundaries(t *testing.T) {
	cases := []struct {
		a1, a2, b1, b2 int
		want           bool
	}{
		{1, 3, 4, 6, false}, // adjacent below
		{4, 6, 1, 3, false}, // adjacent above
		{1, 4, 4, 6, true},  // shared last/first line
		{4, 6, 1, 4, true},  // shared first/last line
		{2, 3, 1, 6, true},  // nested
		{1, 6, 2, 3, true},  // enclosing
	}
	for _, c := range cases {
		if got := overlaps(c.a1, c.a2, c.b1, c.b2); got != c.want {
			t.Errorf("overlaps(%d,%d,%d,%d) = %v", c.a1, c.a2, c.b1, c.b2, got)
		}
	}
}

func TestBodyMarkerCheckMatchesRenderRules(t *testing.T) {
	for _, l := range []string{"<!-- sedum:x {} -->", ";; /sedum:x", "-- sedum:x"} {
		if !hasSedumMarker(l) || !render.IsMarkerLine(l) {
			t.Errorf("%q not recognised", l)
		}
	}
	for _, l := range []string{"see `sedum:x`", "sedum:x", "// note about sedum: later"} {
		if hasSedumMarker(l) != render.IsMarkerLine(l) {
			t.Errorf("%q: converge and render disagree", l)
		}
	}
}

type nilStore struct{}

func (*nilStore) Current(string) (records.Pin, error)  { panic("called on nil") }
func (*nilStore) Snapshot(string, int) ([]byte, error) { panic("called on nil") }

func TestTypedNilRecordsIsErrNoRecords(t *testing.T) {
	c, _ := setup(t, verdictGate{evidence.TestRun, evidence.Pass})
	var s *nilStore
	c.Records = s
	if _, err := c.Converge(context.Background(), cand("func F() {}")); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("err = %v", err)
	}
}

// A spec edit while gates run moves the record's spec hash: not applied.
type specEditGate struct{ s fakeStore }

func (g specEditGate) Name() string        { return "spec-edit" }
func (g specEditGate) Kind() evidence.Kind { return evidence.TestRun }
func (g specEditGate) Run(context.Context, string) (evidence.Verdict, string, error) {
	p := g.s["prov-1"]
	p.SpecHash = "s2"
	g.s["prov-1"] = p
	return evidence.Pass, "", nil
}

func TestSpecEditDuringGatesIsNotApplied(t *testing.T) {
	c, root := setup(t)
	c.Gates = []Gate{specEditGate{c.Records.(fakeStore)}}
	if d, err := c.Converge(context.Background(), cand("func F() {}")); !errors.Is(err, ErrStalePin) || d.Applied {
		t.Fatalf("%+v %v", d, err)
	}
	if read(t, root) != "package app\n" {
		t.Fatal("applied against edited specs")
	}
}
