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
		Model: m, Profile: lang.Go, Spec: spec, Conventions: "conv",
		Check: func(context.Context, []DeclIn) (string, error) { f.check++; return f.checkOut, nil },
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
	for name, files := range map[string]string{
		"unknown section": `{"path":"x.go","section":"app › Nope"},` + fileOne + "," + fileTwo,
		"record key":      `{"path":"x.go","record":"prov-1"},` + fileOne + "," + fileTwo,
		"section no file": fileOne,
		"stub no doc":     strings.Replace(fileTwo, `"doc":"answers PING"`, `"doc":""`, 1) + "," + fileOne,
		"duplicate path":  fileOne + "," + fileOne + "," + fileTwo,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			m := &scripted{answers: []string{answerJSON(files, goodOwners)}}
			c := f.cfg(m)
			c.Retries = 1
			m.answers = append(m.answers, m.answers[0])
			if _, err := Draft(context.Background(), c); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
			if f.check+f.scratch+f.build != 0 {
				t.Error("structure is checked before any subprocess")
			}
		})
	}
	// unknown JSON fields are not accepted either
	f := &fakes{}
	m := &scripted{answers: []string{"```json\n{\"files\":[],\"owners\":{},\"extra\":1}\n```"}}
	c := f.cfg(m)
	c.Retries = 1
	m.answers = append(m.answers, m.answers[0])
	if _, err := Draft(context.Background(), c); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
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
	for _, want := range []string{"the model's claim", "UNOWNED: 2 of 3", "S2, S3", "ignored claim naming nothing", "one.go#accept [claimed]"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if j, err := res.ReportJSON(); err != nil || !strings.Contains(string(j), `"unowned": 2`) {
		t.Errorf("%v %s", err, j)
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
		"const call":      {"a.go", "const", "L", `const L = len("a")`},
		"const conv":      {"a.go", "const", "D", "const D = time.Duration(5)"},
		"var call":        {"a.go", "var", "x", "var x = make(map[string]int)"},
		"var func lit":    {"a.go", "var", "f", "var f = func() int { return 1 }"},
		"var filled":      {"a.go", "var", "m", `var m = map[string]int{"a": 1}`},
		"var value":       {"a.go", "var", "v", "var v = 5"},
		"array len call":  {"a.go", "type", "A", `type A [len("ab")]int`},
		"a func":          {"a.go", "type", "F", "func F() {}"},
		"a method":        {"a.go", "type", "F", "func (s *S) F() {}"},
		"kind mismatch":   {"a.go", "type", "K", "const K = 1"},
		"does not parse":  {"a.go", "type", "P", "type P struct {"},
		"import in decls": {"a.go", "type", "I", `import "os"`},
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
