package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/records"
)

const soID = "prov-2026-aaaa0001"
const soOther = "prov-2026-aaaa0002"
const soBody = "func F() {}\n"

type soEnv struct {
	rec, root, cfg, log string
	store               records.Store
	render              bool
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
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

// newSoEnv builds a records repo (with a spec), a tree holding one synthesis
// region owned by soID, and a config whose tier "low" needs test_run AND
// human_signoff. In render mode a second record exists too, so the skeleton
// can move without moving soID's own gen_hash.
func newSoEnv(t *testing.T, render bool) *soEnv {
	t.Helper()
	e := &soEnv{rec: t.TempDir(), root: t.TempDir(), render: render}
	copyTree(t, "../../internal/records/testdata/with-specs", e.rec)
	if render {
		b, _ := os.ReadFile(filepath.Join(e.rec, "provenance", soID+".yml"))
		os.WriteFile(filepath.Join(e.rec, "provenance", soOther+".yml"),
			[]byte(strings.Replace(string(b), soID, soOther, 1)), 0o644)
		os.MkdirAll(filepath.Join(e.rec, ".graft"), 0o755)
		idx := `{"records":{"` + soID + `":{"section":"A","file":"app.go","order":0},"` + soOther + `":{"section":"B","file":"b.go","order":1}}}`
		os.WriteFile(filepath.Join(e.rec, ".graft", "accepted.json"), []byte(idx), 0o644)
	}
	var err error
	if e.store, err = records.OpenDir(e.rec); err != nil {
		t.Fatal(err)
	}
	if render {
		e.setShape(t, soID, "app.go")
		e.setShape(t, soOther, "b.go")
		os.WriteFile(filepath.Join(e.rec, "skeleton.json"), []byte(`{"files":[{"path":"app.go"}]}`), 0o644)
	}
	cur, err := e.store.Current(soID)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := converge.Seed("//", converge.Owner{Record: soID, Version: cur.Version, Gen: cur.GenHash, Tier: "low"}, soBody)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.root, "app.go"), seed, 0o644)
	e.cfg = filepath.Join(e.rec, "graft.json")
	os.WriteFile(e.cfg, []byte(`{"policy":{"low":["test_run","human_signoff"]},"gates":[{"label":"g","argv":["true"],"kind":"test_run","timeout_sec":10}]}`), 0o644)
	e.log = filepath.Join(t.TempDir(), "ev", "evidence.jsonl")
	os.MkdirAll(filepath.Dir(e.log), 0o755)
	return e
}

func (e *soEnv) setShape(t *testing.T, id, file string) {
	t.Helper()
	if _, err := e.store.(records.ShapeStore).SetShape(id, []any{map[string]any{"path": file, "decls": []any{}}}, "test", "test"); err != nil {
		t.Fatal(err)
	}
}

func (e *soEnv) args(extra ...string) []string {
	a := []string{"signoff", "--config", e.cfg, "--records", e.rec, "--log", e.log, "--root", e.root,
		"--record", soID, "--path", "app.go", "--by", "alice"}
	if e.render {
		a = append(a, "--render")
	}
	return append(a, extra...)
}

func (e *soEnv) signoff(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	return runWith("", e.args(append([]string{"--yes"}, extra...)...))
}

// skeleton is the skeleton hash the way records computes it, from every
// indexed record's shape (an independent computation from the one signoff uses).
func (e *soEnv) skeleton(t *testing.T) string {
	t.Helper()
	if !e.render {
		return ""
	}
	var pins []records.Pin
	for _, id := range []string{soID, soOther} {
		p, err := e.store.Current(id)
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, p)
	}
	h, err := records.SkeletonHash("go", pins)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// converge runs the real converger (the code path graft converge and graft run
// use) on body, with the pins the real flows would set.
func (e *soEnv) converge(t *testing.T, body string) (converge.Decision, error) {
	t.Helper()
	_, gates, err := loadConvergeConfig(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	log, err := evidence.Open(e.log)
	if err != nil {
		t.Fatal(err)
	}
	cand, err := converge.NewCandidate(e.store, soID, "app.go", "low", body)
	if err != nil {
		t.Fatal(err)
	}
	cand.SkeletonHash = e.skeleton(t)
	cfg, _, _ := loadConvergeConfig(e.cfg)
	c := converge.Converger{Root: e.root, Records: e.store, Log: log, Policy: evidence.Policy(cfg.Policy), Gates: gates}
	return c.Converge(context.Background(), cand)
}

func (e *soEnv) lastSignoff(t *testing.T) evidence.Evidence {
	t.Helper()
	log, err := evidence.Open(e.log)
	if err != nil {
		t.Fatal(err)
	}
	var got evidence.Evidence
	for _, x := range log.All() {
		if x.Kind == evidence.HumanSignoff {
			got = x
		}
	}
	if got.ID == "" {
		t.Fatal("no human_signoff in the log")
	}
	return got
}

// curPin is the pin the evidence of the current tree would have.
func (e *soEnv) curPin(t *testing.T) evidence.Pin {
	t.Helper()
	cur, err := e.store.Current(soID)
	if err != nil {
		t.Fatal(err)
	}
	regs, _ := converge.ReadRegions(e.root)
	body, err := converge.RegionBody(e.root, regs[0])
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Pin{RecordID: soID, Version: cur.Version, GenHash: cur.GenHash, RegionHash: converge.RegionHash(body),
		SpecHash: cur.SpecHash, SkeletonHash: e.skeleton(t)}
}

func TestSignoffSatisfiesTier(t *testing.T) {
	for _, render := range []bool{false, true} {
		name := map[bool]string{false: "plain", true: "render"}[render]
		t.Run(name, func(t *testing.T) {
			e := newSoEnv(t, render)
			d, err := e.converge(t, soBody)
			if err != nil || d.Applied || len(d.Result.Missing) != 1 || d.Result.Missing[0] != evidence.HumanSignoff {
				t.Fatalf("before signoff: err %v applied %v result %+v", err, d.Applied, d.Result)
			}
			code, out, errs := e.signoff(t, "--note", "read it")
			if code != 0 || !strings.Contains(out, "signed off app.go") || !strings.Contains(out, "not authenticated") {
				t.Fatalf("exit %d\n%s%s", code, out, errs)
			}
			// Judged fresh by converge's own code path, not signoff's.
			d, err = e.converge(t, soBody)
			if err != nil || !d.Applied || !d.Result.Satisfied {
				t.Fatalf("after signoff: err %v applied %v result %+v", err, d.Applied, d.Result)
			}
			so := e.lastSignoff(t)
			if so.Verdict != evidence.Pass || so.By != "alice" || so.Detail != "read it" {
				t.Fatalf("entry %+v", so)
			}
			if !reflect.DeepEqual(so.Pin, e.curPin(t)) {
				t.Fatalf("pin %+v\nwant %+v", so.Pin, e.curPin(t))
			}
			if render && so.Pin.SkeletonHash == "" || !render && so.Pin.SkeletonHash != "" {
				t.Fatalf("skeleton hash %q in %s mode", so.Pin.SkeletonHash, name)
			}
			// The chain verifies after the signoff.
			log, err := evidence.Open(e.log)
			if err != nil {
				t.Fatal(err)
			}
			if err := log.Verify(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSignoffGoesStaleWithEachPinnedInput(t *testing.T) {
	type edit struct {
		name   string
		render bool
		do     func(t *testing.T, e *soEnv)
		moved  []string
		resign bool // the region can be signed off again (its marker is not behind the record)
	}
	edits := []edit{
		{"body", false, func(t *testing.T, e *soEnv) {
			// A new body, written the way graft writes one (a marker that matches it).
			regs, _ := converge.ReadRegions(e.root)
			o := regs[0].Owner
			seed, err := converge.Seed("//", o, "func G() {}\n")
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(e.root, "app.go"), seed, 0o644)
		}, []string{"region_hash"}, true},
		{"gen_hash", false, func(t *testing.T, e *soEnv) {
			p := filepath.Join(e.rec, "provenance", soID+".yml")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, []byte(strings.Replace(string(b), "Add the widget.", "Add the gadget.", 1)), 0o644)
		}, []string{"gen_hash"}, false},
		{"spec", false, func(t *testing.T, e *soEnv) {
			os.WriteFile(filepath.Join(e.rec, "specs", "widget.md"), []byte("# Widget\n\n- the widget renders twice\n"), 0o644)
		}, []string{"spec_hash"}, true},
		{"skeleton", true, func(t *testing.T, e *soEnv) {
			// Another record's shape: this record's gen_hash does not move.
			if _, err := e.store.(records.ShapeStore).SetShape(soOther, []any{map[string]any{"path": "b.go", "decls": []any{map[string]any{"kind": "const", "name": "x", "code": "const x = 1"}}}}, "t", "t"); err != nil {
				t.Fatal(err)
			}
		}, []string{"skeleton_hash"}, true},
	}
	for _, c := range edits {
		t.Run(c.name, func(t *testing.T) {
			e := newSoEnv(t, c.render)
			if code, out, errs := e.signoff(t); code != 0 {
				t.Fatalf("exit %d %s%s", code, out, errs)
			}
			so := e.lastSignoff(t)
			c.do(t, e)
			cur := e.curPin(t)
			if got := so.Moved(cur); !reflect.DeepEqual(got, c.moved) {
				t.Fatalf("moved %v, want %v", got, c.moved)
			}
			// converge refuses again: the tier lacks fresh human_signoff.
			// (After a body edit the body in the tree is already the new one.)
			body := soBody
			if c.name == "body" {
				body = "func G() {}\n"
			}
			d, err := e.converge(t, body)
			if err != nil || d.Applied || !hasKind(d.Result.Missing, evidence.HumanSignoff) {
				t.Fatalf("err %v applied %v result %+v", err, d.Applied, d.Result)
			}
			if !c.resign {
				// The region still carries the old gen_hash: graft converge or run
				// must rewrite it before it can be signed off again.
				if code, _, _ := e.signoff(t); code != 2 {
					t.Fatalf("signoff of a region behind its record: exit %d, want 2", code)
				}
				return
			}
			// And signing off again makes it satisfied.
			if code, out, errs := e.signoff(t); code != 0 {
				t.Fatalf("re-signoff exit %d %s%s", code, out, errs)
			}
			if d, err = e.converge(t, body); err != nil || !d.Applied {
				t.Fatalf("after re-signoff: %v %+v", err, d)
			}
		})
	}
}

func hasKind(ks []evidence.Kind, k evidence.Kind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func TestSignoffRefusals(t *testing.T) {
	e := newSoEnv(t, false)
	app := filepath.Join(e.root, "app.go")
	good, _ := os.ReadFile(app)

	check := func(name string, args []string) {
		t.Helper()
		code, out, errs := runWith("", args)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2\n%s%s", name, code, out, errs)
		}
		if _, err := os.Stat(e.log); err == nil {
			if log, _ := evidence.Open(e.log); len(log.All()) != 0 {
				t.Errorf("%s: wrote evidence", name)
			}
		}
	}
	with := func(kv ...string) []string {
		a := e.args("--yes")
		for i := 0; i < len(kv); i += 2 {
			for j := range a {
				if a[j] == kv[i] {
					a[j+1] = kv[i+1]
				}
			}
		}
		return a
	}
	// Hand-edited region.
	os.WriteFile(app, []byte(strings.Replace(string(good), "func F() {}", "func F() { hacked() }", 1)), 0o644)
	check("hand-edited", e.args("--yes"))
	os.WriteFile(app, good, 0o644)
	// Missing region: no file, a file without one, another record's region.
	check("no file", with("--path", "nope.go"))
	os.WriteFile(filepath.Join(e.root, "plain.go"), []byte("package app\n"), 0o644)
	check("file without region", with("--path", "plain.go"))
	check("other record", with("--record", soOther))
	// Region written for another gen_hash.
	p := filepath.Join(e.rec, "provenance", soID+".yml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "Add the widget.", "Add the gadget.", 1)), 0o644)
	check("gen moved", e.args("--yes"))
	os.WriteFile(p, b, 0o644)
	// Paths, identity, confirmation.
	check("escaping path", with("--path", "../app.go"))
	check("absolute path", with("--path", app))
	check("backslash path", with("--path", `a\app.go`))
	check("empty by", with("--by", "  "))
	check("multiline by", with("--by", "a\nb"))
	check("unknown record", with("--record", "prov-nope"))
	check("no --yes, not a terminal", e.args())
}

func TestSignoffPrompt(t *testing.T) {
	old := stdinIsTTY
	stdinIsTTY = func(io.Reader) bool { return true }
	defer func() { stdinIsTTY = old }()

	e := newSoEnv(t, false)
	code, out, _ := runWith("n\n", e.args())
	if code != 1 || !strings.Contains(out, "nothing written") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(e.log); err == nil {
		if log, _ := evidence.Open(e.log); len(log.All()) != 0 {
			t.Fatal("declined, but evidence written")
		}
	}
	code, out, errs := runWith("y\n", e.args())
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	// What the human saw: the body, the pins, the caveat, the other evidence.
	for _, want := range []string{"func F() {}", "gen_hash", "region_hash", "spec_hash", "skeleton_hash", "NOT authenticated",
		"other fresh evidence", "also requires, with no fresh evidence: test_run"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt lacks %q:\n%s", want, out)
		}
	}
}

func TestSignoffShowsOtherEvidence(t *testing.T) {
	e := newSoEnv(t, false)
	if _, err := e.converge(t, soBody); err != nil {
		t.Fatal(err)
	}
	_, out, _ := e.signoff(t)
	if !strings.Contains(out, "test_run: pass") || strings.Contains(out, "also requires") {
		t.Fatalf("\n%s", out)
	}
}

func TestSignoffRefusesWhenTheRecordMovesDuringThePrompt(t *testing.T) {
	old := stdinIsTTY
	stdinIsTTY = func(io.Reader) bool { return true }
	defer func() { stdinIsTTY = old }()
	e := newSoEnv(t, false)
	p := filepath.Join(e.rec, "specs", "widget.md")
	// The reader hands over the answer only after the spec has been edited.
	r := &moveThenAnswer{move: func() { os.WriteFile(p, []byte("# Widget\n\n- changed while asking\n"), 0o644) }, answer: "y\n"}
	var out, errb strings.Builder
	code := run2(e.args(), r, &out, &errb)
	if code != 3 || !strings.Contains(errb.String(), "changed while you were being asked") {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(e.log); err == nil {
		if log, _ := evidence.Open(e.log); len(log.All()) != 0 {
			t.Fatal("evidence written for a changed record")
		}
	}
}

type moveThenAnswer struct {
	move   func()
	answer string
	done   bool
}

func (m *moveThenAnswer) Read(p []byte) (int, error) {
	if !m.done {
		m.done = true
		m.move()
		return copy(p, m.answer), nil
	}
	return 0, os.ErrClosed
}

func TestSignoffCLIEndToEndJSONAndHelp(t *testing.T) {
	e := newSoEnv(t, true)
	code, out, errs := e.signoff(t, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	i := strings.Index(out, "{")
	var ev evidence.Evidence
	if err := json.Unmarshal([]byte(out[i:]), &ev); err != nil || ev.Kind != evidence.HumanSignoff || ev.Pin.SkeletonHash == "" {
		t.Fatalf("%v\n%s", err, out)
	}
	if time.Since(ev.At) > time.Minute {
		t.Fatal("bad time")
	}
	_, _, help := runWith("", []string{"signoff", "-h"})
	for _, want := range []string{"NOT authenticated", "exit codes", "--yes"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	// A second command, in the same log, still chains.
	if code, _, errs := e.signoff(t); code != 0 {
		t.Fatal(errs)
	}
	log, _ := evidence.Open(e.log)
	if err := log.Verify(); err != nil || len(log.All()) != 2 {
		t.Fatalf("%v %d", err, len(log.All()))
	}
}
