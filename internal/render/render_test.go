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

func (f *fakeSedum) Version(context.Context) (string, error) { return MinSedum, nil }
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

func readOne(t *testing.T, content string) Region {
	t.Helper()
	root := t.TempDir()
	write(t, root, "a.go", content)
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
	return rs[0]
}

func TestUnverifiableMarkersAreNotEdited(t *testing.T) {
	cases := map[string]string{
		"v1":      "// sedum:setFunction {\"tier\":\"owned\",\"record\":\"r\",\"kwargs\":{\"a\":1}}\nbody\n// /sedum:setFunction\n",
		"no-body": "// sedum:setFunction {\"v\":2,\"tier\":\"owned\",\"record\":\"r\",\"gen\":\"sha256:a\"}\nbody\n// /sedum:setFunction\n",
		"unknown": "// sedum:setFunction {\"v\":3,\"tier\":\"owned\",\"record\":\"r\",\"body\":\"sha256:zz\"}\nbody\n// /sedum:setFunction\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			r := readOne(t, content)
			if !r.Unverifiable() || r.Edited() {
				t.Fatalf("want unverifiable, not edited: %+v", r)
			}
			root := t.TempDir()
			write(t, root, "a.go", content)
			f := &fakeSedum{}
			if err := Render(context.Background(), f, root, "r.json"); err != nil || f.executed != 1 {
				t.Fatalf("err=%v executed=%d", err, f.executed)
			}
		})
	}
}

func TestVerifiedRegionIsNotUnverifiable(t *testing.T) {
	r := readOne(t, region("owned", "r", "setFunction", "x\n", ""))
	if r.Unverifiable() {
		t.Fatal("v2 region with body hash must be verifiable")
	}
}

func TestReadMarkersErrorsOnUnterminatedRegion(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "// sedum:setFunction {\"v\":2,\"body\":\"sha256:x\"}\nbody\n")
	if _, err := ReadMarkers(root); err == nil || !strings.Contains(err.Error(), "never closed") {
		t.Fatalf("err = %v", err)
	}
	f := &fakeSedum{}
	if err := Render(context.Background(), f, root, "r.json"); err == nil || f.executed != 0 {
		t.Fatalf("Render must fail without calling sedum: err=%v executed=%d", err, f.executed)
	}
}

func TestReadMarkersErrorsOnMismatchedClose(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "// sedum:setFunction {\"v\":2,\"body\":\"sha256:x\"}\nbody\n// /sedum:other\n")
	if _, err := ReadMarkers(root); err == nil || !strings.Contains(err.Error(), "closed by marker") {
		t.Fatalf("err = %v", err)
	}
}

func TestProseMentioningSedumIsNotAMarker(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "Use `// sedum:setFunction {\"v\":2}` to mark.\nSee sedum:setFunction in the docs.\nWe say sedum: hello\n")
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 0 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
}

func TestCRLFBodyIsNormalised(t *testing.T) {
	// The marker hash was computed over LF text; the checkout has CRLF.
	content := region("owned", "r", "setFunction", "line1\nline2\n", "")
	content = strings.ReplaceAll(content, "\n", "\r\n")
	r := readOne(t, content)
	if r.Edited() {
		t.Fatalf("CRLF checkout read as hand edit: %+v", r)
	}
}

func TestMarkdownFencedExampleIsNotAMarker(t *testing.T) {
	root := t.TempDir()
	write(t, root, "DOC.md", "Example:\n\n```\n// sedum:setFunction {\"v\":2,\"body\":\"sha256:x\"}\nbody\n```\n")
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 0 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
}

func TestYAMLRegionIsReadAndHandEditDetected(t *testing.T) {
	root := t.TempDir()
	yml := strings.ReplaceAll(region("owned", "rec-y", "setKey", "name: web\n", ""), "// ", "# ")
	write(t, root, "config/app.yml", yml)
	write(t, root, "ci/docker-compose.yaml", strings.ReplaceAll(yml, "rec-y", "rec-z"))
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 2 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
	for _, r := range rs {
		if r.Edited() {
			t.Fatalf("clean YAML region read as edited: %+v", r)
		}
	}
	write(t, root, "config/app.yml", strings.Replace(yml, "name: web", "name: hacked", 1))
	err = Render(context.Background(), &fakeSedum{}, root, "rec.json")
	if !errors.Is(err, ErrHandEdited) || !strings.Contains(err.Error(), "config/app.yml") {
		t.Fatalf("err = %v, want ErrHandEdited naming config/app.yml", err)
	}
}

func TestProvenanceDirIsSkipped(t *testing.T) {
	root := t.TempDir()
	write(t, root, "provenance/prov-1.yml", "doc: |\n  // sedum:setFunction {\"v\":2}\n# sedum:x {\"v\":2}\n")
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 0 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
}

func TestStrayCloseIsIgnored(t *testing.T) {
	for name, c := range map[string]struct {
		content string
		want    int
	}{
		"stray":        {"// /sedum:x\nplain\n", 0},
		"stray-before": {"// /sedum:x\n" + region("owned", "r", "setFunction", "b\n", ""), 1},
		"html":         {"<!-- /sedum:x -->\n", 0},
	} {
		content, want := c.content, c.want
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "f.txt", content)
			rs, err := ReadMarkers(root)
			if err != nil {
				t.Fatalf("stray close caused error: %v", err)
			}
			if len(rs) != want {
				t.Fatalf("got %d regions, want %d: %v", len(rs), want, rs)
			}
		})
	}
	// An opener written with no space after "//" is still an opener.
	if r := readOne(t, strings.Replace(region("owned", "r", "setFunction", "b\n", ""), "// sedum:", "//sedum:", 1)); r.Owner.RecordID != "r" {
		t.Fatalf("//sedum: opener not recognised: %+v", r)
	}
}

func TestAnchorMarkerIsIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.go", "// sedum:anchor:imports\nimport x\n")
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 0 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
}

func TestEmptyPrefixIsNotAMarker(t *testing.T) {
	root := t.TempDir()
	body := `sedum:setFunction {"v":2,"tier":"owned","record":"r","body":"sha256:x"}` + "\nbody\n"
	write(t, root, "f.txt", body)
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 0 {
		t.Fatalf("unprefixed line parsed as marker: rs=%v err=%v", rs, err)
	}
}

func TestMarkdownFenceVariants(t *testing.T) {
	m := "// sedum:setFunction {\"v\":2,\"body\":\"sha256:x\"}\n"
	for name, doc := range map[string]string{
		"tilde":         "~~~\n" + m + "~~~\n",
		"four-backtick": "````\n```\n" + m + "```\n````\n",
		"tilde-in-bt":   "```\n~~~\n" + m + "```\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "D.md", doc)
			rs, err := ReadMarkers(root)
			if err != nil || len(rs) != 0 {
				t.Fatalf("rs=%v err=%v", rs, err)
			}
		})
	}
	// A real region after a closed fence is still read.
	root := t.TempDir()
	write(t, root, "D.md", "~~~\nx\n~~~\n"+region("owned", "r", "setFunction", "b\n", ""))
	rs, err := ReadMarkers(root)
	if err != nil || len(rs) != 1 {
		t.Fatalf("region after closed fence: rs=%v err=%v", rs, err)
	}
}

func TestRegionsCarryMarkerLines(t *testing.T) {
	src := "package x\n\n" + region("owned", "r", "setFunction", "func A() {}\n", "")
	rs, err := ParseFile("x.go", []byte(src))
	if err != nil || len(rs) != 1 {
		t.Fatalf("%v %v", rs, err)
	}
	if rs[0].Start != 3 || rs[0].End != 5 {
		t.Fatalf("lines %d..%d, want 3..5", rs[0].Start, rs[0].End)
	}
}
