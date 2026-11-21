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
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/records"
)

const soID = "prov-2026-aaaa0001"
const soOther = "prov-2026-aaaa0002"
const soBody = "func F() {}\n"

const soSpec = "# T\n\n## Alpha\n\n- When a client connects, the server sends a greeting.\n\n## Beta\n\n- When a client leaves, the server closes its connection.\n"

type soEnv struct {
	rec, root, cfg, log, spec string
	store                     records.Store
	render                    bool
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
	b, _ := os.ReadFile(filepath.Join(e.rec, "provenance", soID+".yml"))
	os.WriteFile(filepath.Join(e.rec, "provenance", soOther+".yml"),
		[]byte(strings.Replace(string(b), soID, soOther, 1)), 0o644)
	if render {
		// A spec whose two sections map onto the two records, as graft run sees it.
		e.spec = filepath.Join(t.TempDir(), "spec.md")
		os.WriteFile(e.spec, []byte(soSpec), 0o644)
		ds := drafter.Drafts(soSpec)
		if len(ds) != 2 {
			t.Fatalf("fixture spec yields %d drafts", len(ds))
		}
		os.MkdirAll(filepath.Join(e.rec, ".graft"), 0o755)
		idx := pipeline.Index{Records: map[string]pipeline.Entry{
			soID:    {Section: ds[0].Section, File: "app.go", DerivedFrom: ds[0].DerivedFrom, Anchor: ds[0].Anchor, Order: 0},
			soOther: {Section: ds[1].Section, File: "b.go", DerivedFrom: ds[1].DerivedFrom, Anchor: ds[1].Anchor, Order: 1},
		}}
		ib, _ := json.Marshal(idx)
		os.WriteFile(filepath.Join(e.rec, ".graft", "accepted.json"), ib, 0o644)
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
		a = append(a, "--spec", e.spec)
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
	// Plain mode. The render-mode equivalent goes through pipeline.Run
	// (TestSignoffRenderTreeIsFreshToRun).
	e := newSoEnv(t, false)
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
	if so.Pin.SkeletonHash != "" {
		t.Fatalf("plain mode pinned a skeleton hash %q", so.Pin.SkeletonHash)
	}
	// The chain verifies after the signoff.
	log, err := evidence.Open(e.log)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestSignoffRenderModeIsDetectedNotChosen(t *testing.T) {
	r := newSoEnv(t, true)
	// A render tree without --spec: refused, not a skeleton-less sign-off.
	args := r.args("--yes")
	for i, a := range args {
		if a == "--spec" {
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	if code, _, errs := runWith("", args); code != 2 || !strings.Contains(errs, "--spec") {
		t.Fatalf("render tree without --spec: exit %d %s", code, errs)
	}
	// A spec with a section that has no accepted record: run would accept it
	// first, so the skeleton hash is not knowable yet.
	more := filepath.Join(t.TempDir(), "more.md")
	os.WriteFile(more, []byte(soSpec+"\n## Gamma\n\n- When a client asks for the time, the server replies with it.\n"), 0o644)
	if code, _, errs := runWith("", r.args("--yes", "--spec", more)); code != 2 || !strings.Contains(errs, "no accepted record") {
		t.Fatalf("unaccepted section: exit %d %s", code, errs)
	}
	// A spec that no longer has the record's section: refused.
	less := filepath.Join(t.TempDir(), "less.md")
	os.WriteFile(less, []byte("# T\n\n## Beta\n\n- When a client leaves, the server closes its connection.\n"), 0o644)
	if code, _, errs := runWith("", r.args("--yes", "--spec", less)); code != 2 {
		t.Fatalf("record not in spec: exit %d %s", code, errs)
	}
	if _, err := os.Stat(r.log); err == nil {
		if log, _ := evidence.Open(r.log); len(log.All()) != 0 {
			t.Fatal("refusals wrote evidence")
		}
	}
	// A plain tree given --spec: refused.
	p := newSoEnv(t, false)
	if code, _, errs := runWith("", p.args("--yes", "--spec", r.spec)); code != 2 || !strings.Contains(errs, "--spec only applies") {
		t.Fatalf("plain tree with --spec: exit %d %s", code, errs)
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
	// A region that exists but belongs to another record, either way round.
	other, _ := converge.Seed("//", converge.Owner{Record: soOther, Version: 1, Gen: "sha256:x", Tier: "low"}, "func H() {}\n")
	os.WriteFile(filepath.Join(e.root, "other.go"), other, 0o644)
	check("region owned by another record", with("--path", "other.go"))
	check("asking for the other record's region in this file", with("--record", soOther))
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

func TestSignoffShowsPriorSignoffsAndEscapesTheBody(t *testing.T) {
	e := newSoEnv(t, false)
	if code, _, errs := e.signoff(t); code != 0 {
		t.Fatal(errs)
	}
	_, out, _ := e.signoff(t)
	if !strings.Contains(out, "already signed off") || !strings.Contains(out, `by "alice"`) {
		t.Fatalf("a duplicate sign-off is not visible:\n%s", out)
	}
	// A body with a terminal escape sequence is shown inert.
	regs, _ := converge.ReadRegions(e.root)
	seed, _ := converge.Seed("//", regs[0].Owner, "func F() {} // \x1b[2J\x07evil\n")
	os.WriteFile(filepath.Join(e.root, "app.go"), seed, 0o644)
	_, out, _ = e.signoff(t)
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, `\x1b[2J\x07evil`) {
		t.Fatalf("control characters reached the terminal:\n%q", out)
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

// With no --log, a sign-off lands in the log graft run reads
// (<repo>/.graft/evidence.jsonl), and the converger reading that log sees it.
func TestSignoffDefaultLogIsTheOneRunReads(t *testing.T) {
	e := newSoEnv(t, false)
	var args []string
	for i := 0; i < len(e.args("--yes")); i++ {
		a := e.args("--yes")
		if a[i] == "--log" {
			i++
			continue
		}
		args = append(args, a[i])
	}
	code, out, errs := runWith("", args)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	runLog := evidenceLogPath(e.rec, "") // what graft run opens when --log is not given
	if !strings.Contains(out, runLog) {
		t.Fatalf("output does not name the log %s:\n%s", runLog, out)
	}
	e.log = runLog
	if so := e.lastSignoff(t); so.Pin.RecordID != soID {
		t.Fatalf("%+v", so)
	}
	d, err := e.converge(t, soBody)
	if err != nil || !d.Applied {
		t.Fatalf("converger on run's log: %v %+v", err, d)
	}
	if _, err := os.Stat(filepath.Join(".graft", "evidence.jsonl")); err == nil {
		t.Fatal("wrote ./.graft/evidence.jsonl in the current directory")
	}
}
