// Command smoke is ledger-lite's VISIBLE smoke suite: one or two behaviour
// checks per spec section, shown to the model and run as a gate while graft
// generates the server. It is written from spec/ledger-lite.md alone and is
// deliberately small; it is never a score. The held-out oracle (../oracle)
// is the only measure, and it tags the checks whose behaviour a smoke check
// also exercises (oracle -list; 12 of 42), so the overlap is stated.
//
// It runs in the generated tree: it builds a copy holding only the sections
// generated so far (a section still holding graft's "not converged"
// placeholder is left out), starts it, and runs those sections' checks.
//
//	go run <path>/smoke.go          # checks for the sections present
//	go run <path>/smoke.go -all     # every check (used against the reference)
//	go run <path>/smoke.go -bin S   # test an existing server binary
//	go run <path>/smoke.go -only transfers.go -json
//
// Stable interface: exit 0 when every selected check passes, 1 otherwise;
// "smoke: X/Y checks passed" first and, when anything failed, again last;
// -json prints {"checks":N,"passed":M,"failed":[...]}.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type check struct {
	file string // the section's file; the check runs once it is generated
	name string
	run  func(addr string) error
}

var checks = []check{
	{"running.go", "an undefined path is 404 not found", func(a string) error {
		return wantErr(a, 404, "not found", "GET", "/no/such/path", "")
	}},
	{"accounts.go", "an account is created with an empty view", func(a string) error {
		id := uniq("acct")
		m, err := want(a, 201, "POST", "/accounts", `{"id":"`+id+`"}`)
		if err != nil {
			return err
		}
		return fields(m, map[string]any{"id": id, "balance": 0.0, "held": 0.0, "available": 0.0, "frozen": false})
	}},
	{"deposits_and_withdrawals.go", "a deposit raises the balance", func(a string) error {
		id, err := account(a, 0)
		if err != nil {
			return err
		}
		m, err := want(a, 200, "POST", "/accounts/"+id+"/deposit", `{"amount":75}`)
		if err != nil {
			return err
		}
		return fields(m, map[string]any{"balance": 75.0, "available": 75.0})
	}},
	{"deposits_and_withdrawals.go", "withdrawing more than the balance is 409", func(a string) error {
		id, err := account(a, 5)
		if err != nil {
			return err
		}
		return wantErr(a, 409, "insufficient funds", "POST", "/accounts/"+id+"/withdraw", `{"amount":6}`)
	}},
	{"transfers.go", "a transfer moves money and is numbered", func(a string) error {
		from, err := account(a, 40)
		if err != nil {
			return err
		}
		to, err := account(a, 0)
		if err != nil {
			return err
		}
		m, err := want(a, 201, "POST", "/transfers", `{"from":"`+from+`","to":"`+to+`","amount":15}`)
		if err != nil {
			return err
		}
		if id, _ := m["id"].(string); !strings.HasPrefix(id, "t") {
			return fmt.Errorf("transfer id %v, want t<n>", m["id"])
		}
		v, err := want(a, 200, "GET", "/accounts/"+to, "")
		if err != nil {
			return err
		}
		return fields(v, map[string]any{"balance": 15.0})
	}},
	{"holds.go", "a hold raises held, not the balance", func(a string) error {
		id, err := account(a, 50)
		if err != nil {
			return err
		}
		m, err := want(a, 201, "POST", "/accounts/"+id+"/holds", `{"amount":20}`)
		if err != nil {
			return err
		}
		if err := fields(m, map[string]any{"account": id, "amount": 20.0}); err != nil {
			return err
		}
		v, err := want(a, 200, "GET", "/accounts/"+id, "")
		if err != nil {
			return err
		}
		return fields(v, map[string]any{"balance": 50.0, "held": 20.0, "available": 30.0})
	}},
	{"journal.go", "a deposit and a transfer appear in the account's journal", func(a string) error {
		from, err := account(a, 30)
		if err != nil {
			return err
		}
		to, err := account(a, 0)
		if err != nil {
			return err
		}
		if _, err := want(a, 201, "POST", "/transfers", `{"from":"`+from+`","to":"`+to+`","amount":10}`); err != nil {
			return err
		}
		m, err := want(a, 200, "GET", "/accounts/"+from+"/journal", "")
		if err != nil {
			return err
		}
		es, _ := m["entries"].([]any)
		if len(es) != 2 {
			return fmt.Errorf("%s's journal has %d entries, want 2 (deposit, transfer): %v", from, len(es), m)
		}
		if err := fields(es[0].(map[string]any), map[string]any{"account": from, "delta": 30.0, "kind": "deposit"}); err != nil {
			return err
		}
		return fields(es[1].(map[string]any), map[string]any{"account": from, "delta": -10.0, "kind": "transfer"})
	}},
	{"freezing.go", "a frozen account says so", func(a string) error {
		id, err := account(a, 0)
		if err != nil {
			return err
		}
		m, err := want(a, 200, "POST", "/accounts/"+id+"/freeze", "")
		if err != nil {
			return err
		}
		return fields(m, map[string]any{"frozen": true})
	}},
	{"freezing.go", "a frozen account refuses a withdrawal with 423", func(a string) error {
		id, err := account(a, 10)
		if err != nil {
			return err
		}
		if _, err := want(a, 200, "POST", "/accounts/"+id+"/freeze", ""); err != nil {
			return err
		}
		return wantErr(a, 423, "account frozen", "POST", "/accounts/"+id+"/withdraw", `{"amount":1}`)
	}},
}

var seq atomic.Int64

func uniq(base string) string { return fmt.Sprintf("%s%d", base, seq.Add(1)) }

var client = &http.Client{Timeout: 5 * time.Second}

// want sends a request and requires a status; it returns the JSON object body.
func want(addr string, status int, method, path, body string) (map[string]any, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, "http://"+addr+path, rd)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.NewDecoder(bytes.NewReader(b)).Decode(&m)
	if resp.StatusCode != status {
		return m, fmt.Errorf("%s %s %s: status %d, want %d (body %s)", method, path, body, resp.StatusCode, status, strings.TrimSpace(string(b)))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return m, fmt.Errorf("%s %s: Content-Type %q, want application/json", method, path, ct)
	}
	return m, nil
}

func wantErr(addr string, status int, msg, method, path, body string) error {
	m, err := want(addr, status, method, path, body)
	if err != nil {
		return err
	}
	if m["error"] != msg {
		return fmt.Errorf("%s %s: body %v, want error %q", method, path, m, msg)
	}
	return nil
}

// account creates a fresh account, deposits n if n > 0, and returns its id.
// Deposits exist only once deposits_and_withdrawals.go is generated, which
// every section after it can rely on (sections are generated in spec order).
func account(addr string, n int) (string, error) {
	id := uniq("acct")
	if _, err := want(addr, 201, "POST", "/accounts", `{"id":"`+id+`"}`); err != nil {
		return "", err
	}
	if n > 0 {
		if _, err := want(addr, 200, "POST", "/accounts/"+id+"/deposit", fmt.Sprintf(`{"amount":%d}`, n)); err != nil {
			return "", err
		}
	}
	return id, nil
}

// fields requires each key to have the wanted value (JSON numbers are float64).
func fields(m map[string]any, wantv map[string]any) error {
	for k, v := range wantv {
		if m[k] != v {
			return fmt.Errorf("field %q is %v, want %v (in %v)", k, m[k], v, m)
		}
	}
	return nil
}

const placeholder = "graft: not converged"

// generated reports whether a section's file exists and is not a seed.
func generated(file, ext string) bool {
	stem := strings.TrimSuffix(file, ".go")
	if b, err := os.ReadFile(stem + "_synth" + ext); err == nil {
		return !strings.Contains(string(b), placeholder)
	}
	b, err := os.ReadFile(stem + ext)
	return err == nil && !strings.Contains(string(b), placeholder)
}

// heavyDirs are dependency directories the copy links rather than copies.
var heavyDirs = map[string]bool{"node_modules": true, ".venv": true, "venv": true, "vendor": true}

// linkDir symlinks dst to the directory src and tells the walk to skip it.
func linkDir(src, dst string) error {
	if err := os.Symlink(src, dst); err != nil {
		return err
	}
	return filepath.SkipDir
}

// copyLink recreates the link at p (inside the tree rooted at srcRoot) as
// dst. A link into the tree stays relative, so the copy never reads the
// original tree (where left-out sections still exist); a link out of the
// tree (a pnpm-style node_modules) keeps its absolute target. Dangling links
// and loops are dropped: the tree could not use them either.
func copyLink(srcRoot, p, dst string) error {
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return nil
	}
	if in, err := filepath.Rel(srcRoot, target); err == nil && in != ".." && !strings.HasPrefix(in, ".."+string(filepath.Separator)) {
		rel, err := filepath.Rel(dir, target)
		if err != nil {
			return nil
		}
		return os.Symlink(rel, dst)
	}
	return os.Symlink(target, dst)
}

// copyConverged copies the tree at src to dst, leaving out every section
// that still holds the placeholder: <stem><ext> and <stem>_synth<ext> go
// together. It returns the stems it left out. .git is skipped.
func copyConverged(src, dst, ext string) ([]string, error) {
	skip := map[string]bool{}
	names, err := filepath.Glob(filepath.Join(src, "*"+ext))
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(b), placeholder) {
			skip[strings.TrimSuffix(strings.TrimSuffix(filepath.Base(n), ext), "_synth")] = true
		}
	}
	srcRoot, err := filepath.Abs(src)
	if err == nil {
		srcRoot, err = filepath.EvalSymlinks(srcRoot)
	}
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if heavyDirs[d.Name()] && rel != "." {
				// Dependencies, not sections: link instead of copying.
				abs, err := filepath.Abs(p)
				if err != nil {
					return err
				}
				return linkDir(abs, filepath.Join(dst, rel))
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		// A left-out section stays out whether its files are regular files or links.
		if filepath.Dir(rel) == "." && strings.HasSuffix(rel, ext) {
			stem := strings.TrimSuffix(strings.TrimSuffix(rel, ext), "_synth")
			if skip[stem] {
				return nil
			}
		}
		if d.Type()&os.ModeSymlink != 0 {
			return copyLink(srcRoot, p, filepath.Join(dst, rel))
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// Keep the mode, so an executable script stays executable.
		return os.WriteFile(filepath.Join(dst, rel), b, info.Mode().Perm())
	})
	var out []string
	for s := range skip {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, err
}

// sameSection: -only accepts a section file in any language (channels.go,
// channels.py) or its bare stem.
func sameSection(file, only string) bool {
	stem := func(s string) string { return strings.TrimSuffix(s, filepath.Ext(s)) }
	return stem(file) == stem(only)
}

// nothingPassed prints the summary for a server that never ran, first and
// last like a normal run, and exits 1.
func nothingPassed(n int, asJSON bool, why string) {
	if asJSON {
		b, _ := json.Marshal(map[string]any{"checks": n, "passed": 0, "failed": []string{why}})
		fmt.Println(string(b))
	} else {
		fmt.Printf("smoke: 0/%d checks passed\n%s\nsmoke: 0/%d checks passed\n", n, strings.TrimRight(why, "\n"), n)
	}
	os.Exit(1)
}

func main() {
	every := flag.Bool("all", false, "run every check, whatever has been generated")
	bin := flag.String("bin", "", "server binary to test (default: build the tree in the current directory)")
	extra := flag.String("server-args", "", "extra arguments for the server, space-separated (e.g. -v2 for the reference)")
	only := flag.String("only", "", "run only the checks of this section file (e.g. channels.go)")
	ext := flag.String("ext", ".go", "source file extension of the tree under test (which sections are generated)")
	cmdJSON := flag.String("cmd", "", `start the server with this JSON argv in the current directory instead of building it, e.g. ["python3","main.py"]; "-addr A" is appended unless an argument holds {addr}`)
	asJSON := flag.Bool("json", false, `print {"checks":N,"passed":M,"failed":[...]} instead of text`)
	flag.Parse()
	var run []check
	for _, c := range checks {
		switch {
		case *only != "":
			if sameSection(c.file, *only) {
				run = append(run, c)
			}
		case *every || generated(c.file, *ext):
			run = append(run, c)
		}
	}
	if len(run) == 0 {
		if *asJSON {
			fmt.Println(`{"checks":0,"passed":0,"failed":[]}`)
			return
		}
		fmt.Println("smoke: no sections generated yet; nothing to check")
		return
	}
	// The server is built and run from a copy of the tree holding only the
	// sections generated so far: under --render every section is seeded up
	// front, and a seeded section's placeholder (its init, say) would panic at
	// start-up before any region could be checked. Sections only register
	// into the core (the conventions), so dropping the unconverged ones leaves
	// a server with exactly the generated sections' behaviour.
	var argv []string
	workDir := "."
	if *bin == "" {
		dir, err := os.MkdirTemp("", "graft-smoke-*")
		if err != nil {
			fail(err.Error())
		}
		defer os.RemoveAll(dir)
		workDir = filepath.Join(dir, "tree")
		dropped, err := copyConverged(".", workDir, *ext)
		if err != nil {
			fail(err.Error())
		}
		if len(dropped) > 0 {
			// stderr: stdout's first and last lines stay the summary.
			fmt.Fprintf(os.Stderr, "smoke: running without the sections not generated yet: %s\n", strings.Join(dropped, ", "))
		}
	}
	switch {
	case *cmdJSON != "":
		if err := json.Unmarshal([]byte(*cmdJSON), &argv); err != nil || len(argv) == 0 {
			fail("smoke: -cmd must be a non-empty JSON array of strings")
		}
	case *bin != "":
		argv = []string{*bin}
	default:
		server := filepath.Join(filepath.Dir(workDir), "server")
		b := exec.Command("go", "build", "-o", server, ".")
		b.Dir = workDir
		if out, err := b.CombinedOutput(); err != nil {
			// Same summary shape as a run, so a ranker can read it: nothing passed.
			nothingPassed(len(run), *asJSON, "the server does not build:\n"+string(out))
		}
		argv = []string{server}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err.Error())
	}
	addr := l.Addr().String()
	l.Close()
	full, placed := make([]string, len(argv)), false
	for i, a := range argv {
		full[i] = strings.ReplaceAll(a, "{addr}", addr)
		placed = placed || full[i] != a
	}
	if !placed {
		full = append(full, "-addr", addr)
	}
	full = append(full, strings.Fields(*extra)...)
	srv := exec.Command(full[0], full[1:]...)
	srv.Dir = workDir
	var srvErr strings.Builder
	srv.Stderr = &srvErr
	// An interpreter's children may hold stderr open after the server is
	// killed: kill the whole process group, and never wait on the pipe forever.
	srv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	srv.WaitDelay = 2 * time.Second
	if err := srv.Start(); err != nil {
		nothingPassed(len(run), *asJSON, "the server does not start: "+err.Error())
	}
	defer func() { killGroup(srv); srv.Wait() }()
	up := false
	for i := 0; i < 50 && !up; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			up = true
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !up {
		killGroup(srv)
		srv.Wait()
		nothingPassed(len(run), *asJSON, "the server never listened on -addr:\n"+tailOf(srvErr.String(), 1500))
	}
	var failed []string
	for _, c := range run {
		if err := c.run(addr); err != nil {
			failed = append(failed, fmt.Sprintf("[%s] %s: %v", c.file, c.name, err))
		}
	}
	if *asJSON {
		b, _ := json.Marshal(map[string]any{"checks": len(run), "passed": len(run) - len(failed), "failed": nonNil(failed)})
		fmt.Println(string(b))
	} else {
		fmt.Printf("smoke: %d/%d checks passed\n", len(run)-len(failed), len(run))
	}
	if len(failed) > 0 {
		if !*asJSON {
			fmt.Println(strings.Join(failed, "\n"))
			// Repeat the summary last: a gate keeps only the tail of long
			// output, and a ranker reads the summary from it.
			fmt.Printf("smoke: %d/%d checks passed\n", len(run)-len(failed), len(run))
		}
		killGroup(srv)
		srv.Wait()
		os.Exit(1)
	}
}

// killGroup kills the server and every process it started.
func killGroup(c *exec.Cmd) {
	if c.Process != nil {
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		c.Process.Kill()
	}
}

func tailOf(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func fail(msg string) {
	fmt.Println(msg)
	os.Exit(1)
}
