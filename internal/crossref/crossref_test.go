package crossref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	stmts = []Stmt{{Section: "Freezing", Hash: "h1", Type: "constraint", Text: "A frozen account refuses deposits.", Own: "rec-freeze"}}
	recs  = []Rec{
		{ID: "rec-freeze", Section: "Freezing", File: "freezing.go", Intent: "Freezing\n- [constraint] A frozen account refuses withdrawals."},
		{ID: "rec-dep", Section: "Deposits and withdrawals", File: "deposits.go", Intent: "Deposits\n- [constraint] A deposit adds to the balance."},
	}
)

func ans(v string, ts ...Target) []Answer {
	return []Answer{{Section: "Freezing", Hash: "h1", Text: "A frozen account refuses deposits.", Verdict: v, Targets: ts}}
}

func good() Target {
	return Target{Record: "rec-dep", Reason: "deposit code must check frozen", Location: "deposits.go: Deposit", Quote: "A deposit adds to the balance"}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers []Answer
		want    string // status of the first answer
		detail  string
	}{
		{"constrains ok", ans(Constrains, good()), OK, ""},
		{"target by section heading", ans(Constrains, func() Target { g := good(); g.Record = "deposits and withdrawals"; return g }()), OK, ""},
		{"local ok", ans(Local), OK, ""},
		{"missing", nil, Unanswered, "no answer"},
		{"unknown record", ans(Constrains, func() Target { g := good(); g.Record = "rec-nope"; return g }()), Dangling, "not a record"},
		{"own record", ans(Constrains, func() Target { g := good(); g.Record = "rec-freeze"; return g }()), Dangling, "own record"},
		{"location outside file", ans(Constrains, func() Target { g := good(); g.Location = "other.go: X"; return g }()), Dangling, "not the record's file"},
		{"no location", ans(Constrains, func() Target { g := good(); g.Location = ""; return g }()), Dangling, "not of the form"},
		{"quote not in record", ans(Constrains, func() Target { g := good(); g.Quote = "invented"; return g }()), Dangling, "does not occur"},
		{"no reason", ans(Constrains, func() Target { g := good(); g.Reason = " "; return g }()), Dangling, "no reason"},
		{"constrains without targets", ans(Constrains), Dangling, "no target"},
		{"local with targets", ans(Local, good()), Dangling, "local with targets"},
		{"bad verdict", ans("maybe"), Dangling, "unknown verdict"},
		{"duplicate", append(ans(Local), ans(Local)...), Dangling, "2 answers"},
		{"altered text", []Answer{{Section: "Freezing", Hash: "h1", Text: "something else", Verdict: Local}}, Dangling, "not the spec's"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Validate(stmts, recs, c.answers, "")
			if got[0].Status != c.want || (c.want == OK) != ok {
				t.Fatalf("status %s ok=%v (%s), want %s", got[0].Status, ok, got[0].Detail, c.want)
			}
			if !strings.Contains(got[0].Detail, c.detail) {
				t.Fatalf("detail %q, want %q", got[0].Detail, c.detail)
			}
		})
	}
}

func TestValidateStatusInFileNotTrusted(t *testing.T) {
	a := ans(Constrains, func() Target { g := good(); g.Record = "rec-nope"; return g }())
	a[0].Status = OK
	if got, ok := Validate(stmts, recs, a, ""); ok || got[0].Status != Dangling {
		t.Fatalf("a hand-written ok status was believed: %v %v", got, ok)
	}
}

func TestValidateStaleAnswer(t *testing.T) {
	a := append(ans(Local), Answer{Section: "Freezing", Hash: "gone", Text: "x", Verdict: Local})
	got, ok := Validate(stmts, recs, a, "")
	if ok || got[len(got)-1].Status != Stale {
		t.Fatalf("got %v ok=%v", got, ok)
	}
}

func TestNewSectionTargetBySectionHeading(t *testing.T) {
	r := append(append([]Rec(nil), recs...), Rec{Section: "Topics"})
	a := ans(Constrains, Target{Record: "Topics", Reason: "r", Location: "topics.go: Set"})
	if got, ok := Validate(stmts, r, a, ""); !ok {
		t.Fatalf("a New section is a valid target: %v", got)
	}
}

func TestLocationIdentifierCheckedAgainstRoot(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "deposits.go"), []byte("package main\nfunc Other() {}\n"), 0o644)
	if _, ok := Validate(stmts, recs, ans(Constrains, good()), root); ok {
		t.Fatal("Deposit is not in deposits.go: must dangle")
	}
	os.WriteFile(filepath.Join(root, "deposits.go"), []byte("package main\nfunc Deposit() {}\n"), 0o644)
	if _, ok := Validate(stmts, recs, ans(Constrains, good()), root); !ok {
		t.Fatal("Deposit is in deposits.go")
	}
}

func TestParse(t *testing.T) {
	good := "text\n```json\n{\"statements\":[{\"id\":\"s1\",\"verdict\":\"local\"}]}\n```\n"
	a, err := Parse(good, stmts)
	if err != nil || len(a) != 1 || a[0].Hash != "h1" || a[0].Text != stmts[0].Text {
		t.Fatalf("%v %v", a, err)
	}
	for name, bad := range map[string]string{
		"unknown id":  "{\"statements\":[{\"id\":\"s9\",\"verdict\":\"local\"}]}",
		"unknown key": "{\"statements\":[{\"id\":\"s1\",\"verdict\":\"local\",\"x\":1}]}",
		"not json":    "I think it is local",
		"trailing":    "{\"statements\":[]} {}",
	} {
		if _, err := Parse(bad, stmts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

type script struct {
	answers []string
	prompts []string
}

func (s *script) Complete(_ context.Context, p string) (string, error) {
	s.prompts = append(s.prompts, p)
	a := s.answers[0]
	if len(s.answers) > 1 {
		s.answers = s.answers[1:]
	}
	return a, nil
}

func TestProposeRetriesOnlyUnparseable(t *testing.T) {
	m := &script{answers: []string{"nonsense", "{\"statements\":[{\"id\":\"s1\",\"verdict\":\"local\"}]}"}}
	p, calls, err := Propose(context.Background(), m, Input{Stmts: stmts, Recs: recs, Retries: 2})
	if err != nil || calls != 2 || p.Answers[0].Status != OK {
		t.Fatalf("%v calls=%d err=%v", p, calls, err)
	}
	if !strings.Contains(m.prompts[1], "refused") {
		t.Fatal("the retry prompt does not say why")
	}
	// an invalid but parseable answer is returned for a human, not retried
	m = &script{answers: []string{"{\"statements\":[]}"}}
	p, calls, err = Propose(context.Background(), m, Input{Stmts: stmts, Recs: recs, Retries: 2})
	if err != nil || calls != 1 || p.Answers[0].Status != Unanswered {
		t.Fatalf("%v calls=%d err=%v", p, calls, err)
	}
	// ... unless strict
	m = &script{answers: []string{"{\"statements\":[]}"}}
	_, calls, err = Propose(context.Background(), m, Input{Stmts: stmts, Recs: recs, Retries: 1, Strict: true})
	if calls != 2 || err != nil {
		t.Fatalf("strict calls=%d err=%v", calls, err)
	}
	m = &script{answers: []string{"nonsense"}}
	if _, _, err := Propose(context.Background(), m, Input{Stmts: stmts, Recs: recs, Retries: 1}); err == nil {
		t.Fatal("no parseable answer must be an error")
	}
}

func TestPromptHasEverythingTheModelNeeds(t *testing.T) {
	p := Prompt(Input{Stmts: stmts, Recs: recs, Conventions: "CONV"})
	for _, w := range []string{Marker, "CONV", "id s1", "A frozen account refuses deposits.", "record rec-dep", "file: deposits.go", "A deposit adds to the balance"} {
		if !strings.Contains(p, w) {
			t.Errorf("prompt lacks %q", w)
		}
	}
}

func TestWarnIsAdvisoryAndNeedsALocalVerdict(t *testing.T) {
	rs := []Rec{
		{ID: "t", Section: "Topics", Intent: "Topics\n- [constraint] TOPIC sets the topic."},
		{ID: "c", Section: "Channels", Intent: "Channels\n- [constraint] JOIN answers 332 then 353."},
	}
	st := []Stmt{{Section: "Topics", Hash: "a", Text: "A joiner receives 332 before 353 and `JOIN` stays in Channels.", Own: "t"}}
	local := []Answer{{Section: "Topics", Hash: "a", Verdict: Local, Status: OK}}
	w := Warn(st, local, rs)
	if len(w) < 2 || !strings.Contains(strings.Join(w, "\n"), "332") || !strings.Contains(strings.Join(w, "\n"), "Channels") {
		t.Fatalf("warnings %v", w)
	}
	if got := Warn(st, []Answer{{Section: "Topics", Hash: "a", Verdict: Constrains, Status: OK}}, rs); len(got) != 0 {
		t.Fatalf("a constrains verdict is not warned about: %v", got)
	}
	// own record's tokens do not count
	only := []Stmt{{Section: "Topics", Hash: "a", Text: "TOPIC sets it.", Own: "t"}}
	if got := Warn(only, local, rs); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestRenderLocationMustBeAStubOrDeclOfTheTargetsSkeletonFile(t *testing.T) {
	rs := []Rec{recs[0], recs[1]}
	rs[1].Render, rs[1].Symbols = true, []string{"init", "Ledger", "Ledger.Post"}
	for _, c := range []struct {
		loc  string
		want string
	}{
		{"deposits.go: init", OK},
		{"deposits.go: Ledger.Post", OK},
		{"deposits.go: (*Ledger).Post()", OK},
		{"deposits.go: Deposit", Dangling}, // not in the skeleton: a skeleton change
	} {
		g := good()
		g.Location = c.loc
		got, _ := Validate(stmts, rs, ans(Constrains, g), "")
		if got[0].Status != c.want {
			t.Errorf("%q: %s (%s), want %s", c.loc, got[0].Status, got[0].Detail, c.want)
		}
		if c.want == Dangling && !strings.Contains(got[0].Detail, "skeleton") {
			t.Errorf("%q: detail %q must point at the skeleton", c.loc, got[0].Detail)
		}
	}
	if d := func() string {
		g := good()
		g.Location = "deposits.go: Deposit"
		got, _ := Validate(stmts, rs, ans(Constrains, g), "")
		return got[0].Detail
	}(); !strings.Contains(d, "needs a skeleton change (graft draft --skeleton)") {
		t.Errorf("detail %q", d)
	}
	// a record with no skeleton file at all
	rs[1].Symbols = nil
	if got, _ := Validate(stmts, rs, ans(Constrains, good()), ""); got[0].Status != Dangling || !strings.Contains(got[0].Detail, "no file for this record") {
		t.Errorf("%+v", got[0])
	}
	if p := Prompt(Input{Stmts: stmts, Recs: []Rec{{ID: "x", Section: "S", Render: true, Symbols: []string{"init"}}}}); !strings.Contains(p, "a location must name one of these): init") {
		t.Error("the prompt must list the skeleton's names in render mode")
	}
}

func TestLocationMustBeFileColonIdentifierWithoutClimbing(t *testing.T) {
	for _, c := range []struct {
		loc, want, detail string
	}{
		{"deposits.go: Deposit", OK, ""},
		{"deposits.go", Dangling, "not of the form"},
		{"deposits.go:", Dangling, "not of the form"},
		{": Deposit", Dangling, "not of the form"},
		{"../deposits.go: Deposit", Dangling, ".. segment"},
		{"x/../deposits.go: Deposit", Dangling, ".. segment"}, // Clean would have resolved it to the record's file
		{"/deposits.go: Deposit", Dangling, "absolute"},
		{"x\\..\\deposits.go: Deposit", Dangling, ".. segment"},
		{"./deposits.go: Deposit", OK, ""},
		{"other.go: Deposit", Dangling, "not the record's file"},
	} {
		g := good()
		g.Location = c.loc
		got, _ := Validate(stmts, recs, ans(Constrains, g), "")
		if got[0].Status != c.want || !strings.Contains(got[0].Detail, c.detail) {
			t.Errorf("%q: %s %q, want %s %q", c.loc, got[0].Status, got[0].Detail, c.want, c.detail)
		}
	}
}

func TestEmptyQuoteIsRefusedUnlessTheTargetIsNew(t *testing.T) {
	g := good()
	g.Quote = ""
	if got, _ := Validate(stmts, recs, ans(Constrains, g), ""); got[0].Status != Dangling {
		t.Fatalf("an existing record needs a quote: %+v", got[0])
	}
	rs := append(append([]Rec(nil), recs...), Rec{Section: "Topics", File: "topics.go"})
	n := Target{Record: "Topics", Reason: "r", Location: "topics.go: Set"}
	if got, _ := Validate(stmts, rs, ans(Constrains, n), ""); got[0].Status != OK {
		t.Fatalf("a New target cannot be quote-checked: %+v", got[0])
	}
}

func TestAnswersAreKeyedBySectionAndHash(t *testing.T) {
	two := []Stmt{
		{Section: "A", Hash: "h", Text: "Every reply must be logged", Own: "ra"},
		{Section: "B", Hash: "h", Text: "Every reply must be logged", Own: "rb"},
	}
	rs := []Rec{{ID: "ra", Section: "A", File: "a.go"}, {ID: "rb", Section: "B", File: "b.go"}, {ID: "rc", Section: "C", File: "c.go", Intent: "C\n- [constraint] C logs"}}
	as := []Answer{
		{Section: "A", Hash: "h", Text: "Every reply must be logged", Verdict: Local},
		{Section: "B", Hash: "h", Text: "Every reply must be logged", Verdict: Local},
	}
	got, ok := Validate(two, rs, as, "")
	if !ok || len(got) != 2 {
		t.Fatalf("%v %+v", ok, got)
	}
	// the same section answered twice is still a duplicate
	if _, ok := Validate(two[:1], rs, append(as[:1:1], as[0]), ""); ok {
		t.Fatal("two answers for one (section, hash) must not pass")
	}
	// an answer for the wrong section is stale, and its statement unanswered
	got, ok = Validate(two[:1], rs, []Answer{{Section: "B", Hash: "h", Text: "x", Verdict: Local}}, "")
	if ok || got[0].Status != Unanswered || got[1].Status != Stale {
		t.Fatalf("%+v", got)
	}
}
