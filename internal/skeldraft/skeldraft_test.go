package skeldraft

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/recording"
)

const spec = `# app

## One

- The server must accept a nick
- A nick must never exceed 16 characters

## Two

- The server must answer a PING with a PONG
`

// scripted is a fake model: it answers from a list and records its prompts.
type scripted struct {
	answers []string
	prompts []string
}

func (m *scripted) Complete(_ context.Context, p string) (string, error) {
	i := len(m.prompts)
	m.prompts = append(m.prompts, p)
	if i >= len(m.answers) {
		return "", errors.New("scripted model: out of answers")
	}
	return m.answers[i], nil
}

// good is a skeleton that covers every statement.
func answerJSON(files, owners string) string {
	return "```json\n{\"files\": [" + files + "], \"owners\": {" + owners + "}}\n```"
}

const fileOne = `{"path":"one.go","section":"app › One","decls":[{"kind":"type","name":"Server","code":"type Server struct{}"}],"stubs":[{"name":"accept","receiver_name":"s","receiver_type":"*Server","doc":"accepts a nick"}]}`
const fileTwo = `{"path":"two.go","section":"app › Two","stubs":[{"name":"ping","doc":"answers PING"}]}`

var goodOwners = `"S1":["one.go#accept"],"S2":["one.go#accept"],"S3":["two.go#ping"]`

type fakes struct {
	check, scratch, build int
	buildOut              string
	checkOut              string
}

func (f *fakes) cfg(m *scripted) Config {
	return Config{
		Model: m, Profile: lang.Go, Spec: spec, Conventions: "conv", Retries: DefaultRetries,
		FileFor: func(s string) string { return strings.ToLower(s[strings.LastIndex(s, " ")+1:]) + ".go" },
		Check:   func(context.Context, []DeclIn) (string, error) { f.check++; return f.checkOut, nil },
		Scratch: func(context.Context, []drafter.Draft, recording.Skeleton) (string, func(), error) {
			f.scratch++
			return "root", func() {}, nil
		},
		Build: func(context.Context, string) (string, error) { f.build++; return f.buildOut, nil },
	}
}

func TestDraftValid(t *testing.T) {
	f := &fakes{}
	m := &scripted{answers: []string{answerJSON(fileOne+","+fileTwo, goodOwners)}}
	res, err := Draft(context.Background(), f.cfg(m))
	if err != nil {
		t.Fatal(err)
	}
	if res.Calls != 1 || len(res.Skeleton.Files) != 2 || len(res.Unowned()) != 0 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Report(), "the model's claim") || !strings.Contains(res.Report(), "UNOWNED: none") {
		t.Errorf("report:\n%s", res.Report())
	}
}

func TestInvalidProposalRetriedThenFails(t *testing.T) {
	f := &fakes{buildOut: "./one.go:3: undefined: Frobnicator"}
	bad := answerJSON(fileOne+","+fileTwo, goodOwners)
	m := &scripted{answers: []string{bad, bad, bad, bad}}
	res, err := Draft(context.Background(), f.cfg(m))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if res.Calls != 3 || len(m.prompts) != 3 || f.build != 3 {
		t.Fatalf("calls=%d prompts=%d builds=%d, want 3 each (2 retries)", res.Calls, len(m.prompts), f.build)
	}
	if len(res.Skeleton.Files) != 0 {
		t.Error("a failed run must not return a skeleton")
	}
	if strings.Contains(m.prompts[0], "Frobnicator") || !strings.Contains(m.prompts[1], "Frobnicator") {
		t.Error("the build output must reach the retry prompt, and only the retry prompt")
	}
	if !strings.Contains(err.Error(), "Frobnicator") {
		t.Errorf("the final error must say why: %v", err)
	}
}

func TestRetriesFlagBounds(t *testing.T) {
	f := &fakes{buildOut: "no"}
	bad := answerJSON(fileOne+","+fileTwo, goodOwners)
	m := &scripted{answers: []string{bad, bad, bad, bad, bad, bad, bad}}
	c := f.cfg(m)
	c.Retries = 5
	if res, _ := Draft(context.Background(), c); res.Calls != 6 {
		t.Errorf("retries 5 made %d calls, want 6", res.Calls)
	}
	c.Retries = 6
	if _, err := Draft(context.Background(), c); err == nil {
		t.Error("retries above the cap must be refused")
	}
}

func TestRecoversOnRetry(t *testing.T) {
	f := &fakes{}
	m := &scripted{answers: []string{"I think the answer is a skeleton", answerJSON(fileOne+","+fileTwo, goodOwners)}}
	res, err := Draft(context.Background(), f.cfg(m))
	if err != nil || res.Calls != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	if !strings.Contains(m.prompts[1], "rejected") {
		t.Error("the retry prompt must carry the rejection")
	}
}

// The cheap rejection comes first: a func decl never reaches the checker
// subprocess, sedum or the build.
func TestFuncKindRejectedBeforeAnyBuild(t *testing.T) {
	f := &fakes{}
	withFunc := `{"path":"one.go","section":"app › One","decls":[{"kind":"func","name":"helper","code":"func helper() int { return 1 }"}],"stubs":[{"name":"accept","doc":"d"}]}`
	m := &scripted{answers: []string{answerJSON(withFunc+","+fileTwo, goodOwners), answerJSON(withFunc+","+fileTwo, goodOwners), answerJSON(withFunc+","+fileTwo, goodOwners)}}
	_, err := Draft(context.Background(), f.cfg(m))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if f.check != 0 || f.scratch != 0 || f.build != 0 {
		t.Fatalf("check=%d scratch=%d build=%d: a func decl must be rejected before any of them", f.check, f.scratch, f.build)
	}
	if !strings.Contains(m.prompts[1], `kind "func"`) {
		t.Errorf("the retry must say why:\n%s", m.prompts[1])
	}
}

func TestStructuralRejections(t *testing.T) {
	good := answerJSON(fileOne+","+fileTwo, goodOwners)
	for name, c := range map[string]struct{ ans, want string }{
		"unknown section": {answerJSON(`{"path":"x.go","section":"app › Nope","stubs":[{"name":"a","doc":"d"}]},`+fileOne+","+fileTwo, goodOwners), "not a section of the spec"},
		// section and path are valid; only the record key is wrong
		"record key":      {answerJSON(strings.Replace(fileOne, `"section"`, `"record":"prov-1","section"`, 1)+","+fileTwo, goodOwners), `never "record"`},
		"section no file": {answerJSON(fileOne, goodOwners), "owns no file"},
		"stub no doc":     {answerJSON(strings.Replace(fileTwo, `"doc":"answers PING"`, `"doc":""`, 1)+","+fileOne, goodOwners), "no doc contract"},
		"duplicate path":  {answerJSON(fileOne+","+fileOne+","+fileTwo, goodOwners), "listed twice"},
		"wrong path":      {answerJSON(strings.Replace(fileOne, "one.go", "unknown.go", 1)+","+fileTwo, goodOwners), "must own exactly the file"},
		// a second file for a section, at a path that is otherwise fine
		"two files":      {answerJSON(fileOne+","+strings.Replace(fileOne, "one.go", "extra.go", 1)+","+fileTwo, goodOwners), "more than one file"},
		"no stub":        {answerJSON(`{"path":"one.go","section":"app › One","decls":[{"kind":"type","name":"S","code":"type S struct{}"}]},`+fileTwo, goodOwners), "at least one stub"},
		"unknown owner":  {answerJSON(fileOne+","+fileTwo, goodOwners+`,"S99":["one.go#accept"]`), "not a statement id"},
		"unknown field":  {strings.Replace(good, `"owners"`, `"extra": 1, "owners"`, 1), "unknown field"},
		"unknown nested": {strings.Replace(good, `"decls"`, `"bogus": true, "decls"`, 1), "unknown field"},
		"trailing data":  {strings.TrimSuffix(good, "\n```") + "\n{}\n```", "trailing"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			m := &scripted{answers: []string{c.ans}}
			cfg := f.cfg(m)
			cfg.Retries = 0
			res, err := Draft(context.Background(), cfg)
			if !errors.Is(err, ErrInvalid) || res.Calls != 1 {
				t.Fatalf("err = %v, calls = %d", err, res.Calls)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error lacks %q: %v", c.want, err)
			}
			if f.check+f.scratch+f.build != 0 {
				t.Error("structure is checked before any subprocess")
			}
		})
	}
}

func TestZeroRetriesMeansOneCall(t *testing.T) {
	f := &fakes{buildOut: "no"}
	bad := answerJSON(fileOne+","+fileTwo, goodOwners)
	m := &scripted{answers: []string{bad, bad, bad}}
	c := f.cfg(m)
	c.Retries = 0
	if res, err := Draft(context.Background(), c); !errors.Is(err, ErrInvalid) || res.Calls != 1 {
		t.Fatalf("calls=%d err=%v, want one call", res.Calls, err)
	}
}

// A broken tool is not a verdict on the proposal: no feedback, no retry.
func TestToolFailureIsNeverRetried(t *testing.T) {
	good := answerJSON(fileOne+","+fileTwo, goodOwners)
	for name, mod := range map[string]func(*Config){
		"check": func(c *Config) {
			c.Check = func(context.Context, []DeclIn) (string, error) { return "", errors.New("boom") }
		},
		"build": func(c *Config) {
			c.Build = func(context.Context, string) (string, error) { return "", errors.New("boom") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			m := &scripted{answers: []string{good, good, good}}
			c := f.cfg(m)
			mod(&c)
			res, err := Draft(context.Background(), c)
			if !errors.Is(err, ErrTool) || errors.Is(err, ErrInvalid) || res.Calls != 1 || len(m.prompts) != 1 {
				t.Fatalf("err=%v calls=%d", err, res.Calls)
			}
		})
	}
}

func TestBrokenGoCheckerIsAToolErrorNotARejection(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	saved := lang.GoDeclCheckSource
	defer func() { lang.GoDeclCheckSource = saved }()
	lang.GoDeclCheckSource = "package main\nfunc main( {\n"
	out, err := ExecCheck(lang.Go)(context.Background(), []DeclIn{{"a.go", "type", "T", "type T struct{}"}})
	if !errors.Is(err, ErrTool) || out != "" {
		t.Fatalf("out=%q err=%v: a checker that does not compile is a tool error", out, err)
	}
}

func TestGoCheckerIgnoresTheUsersGoEnv(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	t.Setenv("GOFLAGS", "-badflag")
	t.Setenv("GOPROXY", "https://invalid.example")
	t.Setenv("GO111MODULE", "on")
	t.Setenv("GOWORK", "/nonexistent/go.work")
	out, err := ExecCheck(lang.Go)(context.Background(), []DeclIn{{"a.go", "type", "T", "type T struct{}"}})
	if err != nil || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestUnownedStatementFlagged(t *testing.T) {
	f := &fakes{}
	// S2 has no owner; S3's claim names a stub that does not exist (dangling).
	m := &scripted{answers: []string{answerJSON(fileOne+","+fileTwo, `"S1":["one.go#accept"],"S3":["two.go#nosuch"]`)}}
	res, err := Draft(context.Background(), f.cfg(m))
	if err != nil {
		t.Fatal(err)
	}
	u := res.Unowned()
	if len(u) != 2 || u[0].Statement.ID != "S2" || u[1].Statement.ID != "S3" {
		t.Fatalf("unowned = %+v", u)
	}
	rep := res.Report()
	for _, want := range []string{"the model's claim", "UNOWNED: 2 of 3", "S2, S3", "DANGLING claim", "one.go#accept [claimed]"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if j, err := res.ReportJSON(); err != nil || !strings.Contains(string(j), `"unowned": 2`) {
		t.Errorf("%v %s", err, j)
	}
}

// One valid claim next to a dangling one is not "unowned", but it is not clean.
func TestDanglingClaimBesideAValidOneIsReported(t *testing.T) {
	f := &fakes{}
	m := &scripted{answers: []string{answerJSON(fileOne+","+fileTwo, `"S1":["one.go#accept","one.go#nosuch"],"S2":["one.go#accept"],"S3":["two.go#ping"]`)}}
	res, err := Draft(context.Background(), f.cfg(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unowned()) != 0 || len(res.Dangling()) != 1 {
		t.Fatalf("unowned=%d dangling=%d", len(res.Unowned()), len(res.Dangling()))
	}
	rep := res.Report()
	if !strings.Contains(rep, "DANGLING: 1 statement(s)") || !strings.Contains(rep, "S1 -> one.go#nosuch") {
		t.Errorf("report:\n%s", rep)
	}
	if j, _ := res.ReportJSON(); !strings.Contains(string(j), `"dangling": 1`) {
		t.Errorf("%s", j)
	}
	// --strict turns it into a retry
	m2 := &scripted{answers: []string{m.answers[0], answerJSON(fileOne+","+fileTwo, goodOwners)}}
	c := f.cfg(m2)
	c.Strict = true
	if res, err := Draft(context.Background(), c); err != nil || res.Calls != 2 || len(res.Dangling()) != 0 {
		t.Fatalf("strict: %v %+v", err, res)
	}
}

func TestStrictRetriesOnUnowned(t *testing.T) {
	f := &fakes{}
	m := &scripted{answers: []string{
		answerJSON(fileOne+","+fileTwo, `"S1":["one.go#accept"]`),
		answerJSON(fileOne+","+fileTwo, goodOwners),
	}}
	c := f.cfg(m)
	c.Strict = true
	res, err := Draft(context.Background(), c)
	if err != nil || res.Calls != 2 || len(res.Unowned()) != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	if !strings.Contains(m.prompts[1], "S2, S3") {
		t.Error("the retry must name the unowned statements")
	}
}

func TestProfileWithoutDeclCheckCannotDraft(t *testing.T) {
	f := &fakes{}
	c := f.cfg(&scripted{})
	c.Profile = lang.Profile{Name: "py", Ext: ".py"}
	if _, err := Draft(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decl_check") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelFailureIsNotInvalid(t *testing.T) {
	f := &fakes{}
	_, err := Draft(context.Background(), f.cfg(&scripted{}))
	if !errors.Is(err, ErrModel) || errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// The Go decl_check, run for real (embedded source, no network).
func TestGoDeclCheck(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	check := ExecCheck(lang.Go)
	ok := []DeclIn{
		{"a.go", "type", "T", "type T struct {\n\tm map[string]func(int) error\n\tc chan int\n}"},
		{"a.go", "const", "Timeout", "const Timeout = time.Second * 5"},
		{"a.go", "const", "A", "const (\n\tA = iota\n\tB\n)"},
		{"a.go", "var", "handlers", "var handlers = map[string]func(s *Server, c *Client){}"},
		{"a.go", "var", "n", "var n int"},
		{"a.go", "var", "s", "var s []string = []string{}"},
	}
	if out, err := check(context.Background(), ok); err != nil || out != "" {
		t.Fatalf("logic-free decls rejected: %v %q", err, out)
	}
	for name, d := range map[string]DeclIn{
		"const call":            {"a.go", "const", "L", `const L = len("a")`},
		"const conv":            {"a.go", "const", "D", "const D = time.Duration(5)"},
		"var call":              {"a.go", "var", "x", "var x = make(map[string]int)"},
		"var func lit":          {"a.go", "var", "f", "var f = func() int { return 1 }"},
		"var filled":            {"a.go", "var", "m", `var m = map[string]int{"a": 1}`},
		"var value":             {"a.go", "var", "v", "var v = 5"},
		"array len call":        {"a.go", "type", "A", `type A [len("ab")]int`},
		"a func":                {"a.go", "type", "F", "func F() {}"},
		"a method":              {"a.go", "type", "F", "func (s *S) F() {}"},
		"kind mismatch":         {"a.go", "type", "K", "const K = 1"},
		"does not parse":        {"a.go", "type", "P", "type P struct {"},
		"type param constraint": {"a.go", "type", "G", `type G[P interface{ ~[len("a")]int }] struct{}`},
		"import in decls":       {"a.go", "type", "I", `import "os"`},
	} {
		out, err := check(context.Background(), []DeclIn{d})
		if err != nil || !strings.Contains(out, "a.go: "+d.Name+": ") {
			t.Errorf("%s: want a rejection naming the decl, got err=%v out=%q", name, err, out)
		}
	}
}

// A decl with logic reaches the model as the checker's own words.
func TestDeclLogicRejectedAndFedBack(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	f := &fakes{}
	logic := `{"path":"one.go","section":"app › One","decls":[{"kind":"var","name":"limit","code":"var limit = compute()"}],"stubs":[{"name":"accept","doc":"d"}]}`
	m := &scripted{answers: []string{answerJSON(logic+","+fileTwo, goodOwners), answerJSON(fileOne+","+fileTwo, goodOwners)}}
	c := f.cfg(m)
	c.Check = ExecCheck(lang.Go)
	res, err := Draft(context.Background(), c)
	if err != nil || res.Calls != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	if f.scratch != 1 || f.build != 1 {
		t.Errorf("scratch=%d build=%d: only the valid second answer may be rendered and built", f.scratch, f.build)
	}
	if p := m.prompts[1]; !strings.Contains(p, "one.go: limit:") || !strings.Contains(p, "initialiser") {
		t.Errorf("the checker's stderr must be fed back:\n%s", p)
	}
}

// GOPROXY, GOWORK and GO111MODULE cannot be observed to matter from a passing
// build, so the pin itself is asserted; GOFLAGS is checked by behaviour above.
func TestGoEnvPinsTheCheckerBuild(t *testing.T) {
	have := strings.Join(goEnv, " ")
	for _, want := range []string{"GOFLAGS=", "GOPROXY=off", "GOWORK=off", "GO111MODULE=off"} {
		if !strings.Contains(have+" ", want+" ") {
			t.Errorf("goEnv lacks %s: %v", want, goEnv)
		}
	}
}
