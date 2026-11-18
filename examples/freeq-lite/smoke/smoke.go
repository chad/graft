// Command smoke is freeq-lite's VISIBLE smoke suite: a handful of behaviour
// checks per spec section, shown to the model and run as a gate while graft
// generates the server. It is written from spec/freeq-lite.md alone, with its
// own scenarios; it is deliberately small, and it is never a score. The
// held-out oracle (../oracle) remains the only measure.
//
// It runs in the generated tree: it builds the server, starts it, and runs
// the checks of every section whose file has been generated so far (a file
// still holding graft's "not converged" placeholder does not count). Each
// region's gate therefore checks its own section and every earlier one.
//
//	go run <path>/smoke.go          # checks for the sections present
//	go run <path>/smoke.go -all     # every check (used against the reference)
//	go run <path>/smoke.go -bin S   # test an existing server binary
//	go run <path>/smoke.go -only channels.go -json   # one section, machine-readable
//
// Stable interface (best-of-N ranks on it): exit 0 when every selected check
// passes, 1 otherwise; -json prints {"checks":N,"passed":M,"failed":[...]}.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type check struct {
	file string // the section's file; the check runs once it is generated
	name string
	run  func(addr string) error
}

var checks = []check{
	{"running.go", "an over-long line is ignored and the connection stays open", func(a string) error {
		c, err := dial(a)
		if err != nil {
			return err
		}
		defer c.close()
		c.send(strings.Repeat("Q", 600))
		if err := c.quiet(); err != nil {
			return err
		}
		// Still open: a write after the long line must not fail.
		if _, err := c.c.Write([]byte("\r\n")); err != nil {
			return fmt.Errorf("connection closed after an over-long line: %w", err)
		}
		return nil
	}},
	{"registration.go", "register and get welcome", func(a string) error {
		c, err := reg(a, nick("rua"))
		if err == nil {
			c.close()
		}
		return err
	}},
	{"registration.go", "bad nick is 432", func(a string) error {
		c, err := dial(a)
		if err != nil {
			return err
		}
		defer c.close()
		c.send("NICK 7up")
		return c.want(":freeq-lite 432 * 7up :Erroneous nickname")
	}},
	{"registration.go", "taken nick is 433", func(a string) error {
		n := nick("sid")
		c1, err := reg(a, n)
		if err != nil {
			return err
		}
		defer c1.close()
		c2, err := dial(a)
		if err != nil {
			return err
		}
		defer c2.close()
		c2.send("NICK " + n)
		return c2.want(":freeq-lite 433 * " + n + " :Nickname is already in use")
	}},
	{"keepalive.go", "ping gets pong", func(a string) error {
		c, err := dial(a)
		if err != nil {
			return err
		}
		defer c.close()
		c.send("PING hello123")
		return c.want(":freeq-lite PONG freeq-lite :hello123")
	}},
	{"channels.go", "join echoes and lists names", func(a string) error {
		n, ch := nick("tess"), "#smk"+fmt.Sprint(seq.Add(1))
		c, err := reg(a, n)
		if err != nil {
			return err
		}
		defer c.close()
		c.send("JOIN " + ch)
		return all(c.want(":"+n+"!"+n+"@freeq-lite JOIN "+ch),
			c.want(":freeq-lite 353 "+n+" = "+ch+" :"+n),
			c.want(":freeq-lite 366 "+n+" "+ch+" :End of /NAMES list"))
	}},
	{"channels.go", "part when not a member is 442", func(a string) error {
		n, ch := nick("uli"), "#nope"+fmt.Sprint(seq.Add(1))
		c, err := reg(a, n)
		if err != nil {
			return err
		}
		defer c.close()
		c.send("PART " + ch)
		return c.want(":freeq-lite 442 " + n + " " + ch + " :You're not on that channel")
	}},
	{"messages.go", "channel message reaches others, not the sender", func(a string) error {
		n1, n2, ch := nick("val"), nick("wim"), "#say"+fmt.Sprint(seq.Add(1))
		c1, err := reg(a, n1)
		if err != nil {
			return err
		}
		defer c1.close()
		c2, err := reg(a, n2)
		if err != nil {
			return err
		}
		defer c2.close()
		c1.send("JOIN " + ch)
		if err := c1.until(":freeq-lite 366 " + n1 + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		c2.send("JOIN " + ch)
		if err := c2.until(":freeq-lite 366 " + n2 + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		if err := c1.until(":" + n2 + "!" + n2 + "@freeq-lite JOIN " + ch); err != nil {
			return err
		}
		c1.send("PRIVMSG " + ch + " :good morning")
		return all(c2.want(":"+n1+"!"+n1+"@freeq-lite PRIVMSG "+ch+" :good morning"), c1.quiet())
	}},
	{"messages.go", "message to nobody is 401", func(a string) error {
		n := nick("xia")
		c, err := reg(a, n)
		if err != nil {
			return err
		}
		defer c.close()
		ghost := nick("nobody")
		c.send("PRIVMSG " + ghost + " :anyone?")
		return c.want(":freeq-lite 401 " + n + " " + ghost + " :No such nick/channel")
	}},
	{"leaving.go", "quit closes and tells channel peers", func(a string) error {
		n1, n2, ch := nick("yan"), nick("zed"), "#bye"+fmt.Sprint(seq.Add(1))
		c1, err := reg(a, n1)
		if err != nil {
			return err
		}
		defer c1.close()
		c2, err := reg(a, n2)
		if err != nil {
			return err
		}
		defer c2.close()
		c1.send("JOIN " + ch)
		if err := c1.until(":freeq-lite 366 " + n1 + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		c2.send("JOIN " + ch)
		if err := c2.until(":freeq-lite 366 " + n2 + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		c1.send("QUIT :see ya")
		return all(c1.until("ERROR :Closing link"), c2.until(":"+n1+"!"+n1+"@freeq-lite QUIT :see ya"))
	}},
	{"everything_else.go", "unknown command is 421", func(a string) error {
		n := nick("abe")
		c, err := reg(a, n)
		if err != nil {
			return err
		}
		defer c.close()
		c.send("WIBBLE now")
		return c.want(":freeq-lite 421 " + n + " WIBBLE :Unknown command")
	}},
	// Only for a spec with a Topics section (spec-v2). Under the v1 spec there
	// is no Topics section, TOPIC is an unknown command (421), and this check
	// never runs, because no topics.go is generated.
	{"topics.go", "topic is set, broadcast and queried", func(a string) error {
		n, ch := nick("bo"), "#tp"+fmt.Sprint(seq.Add(1))
		c, err := reg(a, n)
		if err != nil {
			return err
		}
		defer c.close()
		c.send("JOIN " + ch)
		if err := c.until(":freeq-lite 366 " + n + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		c.send("TOPIC " + ch)
		if err := c.want(":freeq-lite 331 " + n + " " + ch + " :No topic is set"); err != nil {
			return err
		}
		c.send("TOPIC " + ch + " :hello world")
		if err := c.want(":" + n + "!" + n + "@freeq-lite TOPIC " + ch + " :hello world"); err != nil {
			return err
		}
		c.send("TOPIC " + ch)
		return c.want(":freeq-lite 332 " + n + " " + ch + " :hello world")
	}},
}

// ---- client helpers ----

var seq atomic.Int64

func nick(base string) string { return fmt.Sprintf("%s%d", base, seq.Add(1)) }

type conn struct {
	c     net.Conn
	lines chan string
}

func dial(addr string) (*conn, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	k := &conn{c: c, lines: make(chan string, 256)}
	go func() {
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			k.lines <- strings.TrimRight(sc.Text(), "\r")
		}
		close(k.lines)
	}()
	return k, nil
}

func (k *conn) send(l string) { k.c.Write([]byte(l + "\r\n")) }
func (k *conn) close()        { k.c.Close() }

func (k *conn) want(w string) error {
	select {
	case got, ok := <-k.lines:
		if !ok {
			return fmt.Errorf("connection closed, wanted %q", w)
		}
		if got != w {
			return fmt.Errorf("got %q, want %q", got, w)
		}
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("no reply, wanted %q", w)
	}
}

func (k *conn) until(w string) error {
	t := time.After(2 * time.Second)
	for {
		select {
		case got, ok := <-k.lines:
			if !ok {
				return fmt.Errorf("connection closed, wanted %q", w)
			}
			if got == w {
				return nil
			}
		case <-t:
			return fmt.Errorf("never saw %q", w)
		}
	}
}

func (k *conn) quiet() error {
	select {
	case got, ok := <-k.lines:
		if ok {
			return fmt.Errorf("unexpected %q", got)
		}
		return nil
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func reg(addr, n string) (*conn, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, err
	}
	c.send("NICK " + n)
	c.send("USER " + n + " 0 * :" + n)
	if err := c.want(":freeq-lite 001 " + n + " :Welcome to freeq-lite " + n); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func all(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ---- driver ----

const placeholder = "graft: not converged"

// generated reports whether a section's file exists and is not a seed.
func generated(file string) bool {
	stem := strings.TrimSuffix(file, ".go")
	if b, err := os.ReadFile(stem + "_synth.go"); err == nil {
		return !strings.Contains(string(b), placeholder)
	}
	b, err := os.ReadFile(file)
	return err == nil && !strings.Contains(string(b), placeholder)
}

func main() {
	every := flag.Bool("all", false, "run every check, whatever has been generated")
	bin := flag.String("bin", "", "server binary to test (default: build the tree in the current directory)")
	extra := flag.String("server-args", "", "extra arguments for the server, space-separated (e.g. -v2 for the reference)")
	only := flag.String("only", "", "run only the checks of this section file (e.g. channels.go)")
	asJSON := flag.Bool("json", false, `print {"checks":N,"passed":M,"failed":[...]} instead of text`)
	flag.Parse()
	var run []check
	for _, c := range checks {
		switch {
		case *only != "":
			if c.file == *only {
				run = append(run, c)
			}
		case *every || generated(c.file):
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
	server := *bin
	if server == "" {
		dir, err := os.MkdirTemp("", "graft-smoke-*")
		if err != nil {
			fail(err.Error())
		}
		defer os.RemoveAll(dir)
		server = filepath.Join(dir, "server")
		if out, err := exec.Command("go", "build", "-o", server, ".").CombinedOutput(); err != nil {
			// Same summary shape as a run, so a ranker can read it: nothing passed.
			if *asJSON {
				fmt.Printf("{\"checks\":%d,\"passed\":0,\"failed\":[\"the server does not build\"]}\n", len(run))
			} else {
				fmt.Printf("smoke: 0/%d checks passed\nthe server does not build:\n%s", len(run), out)
			}
			os.Exit(1)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err.Error())
	}
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(server, append([]string{"-addr", addr}, strings.Fields(*extra)...)...)
	if err := srv.Start(); err != nil {
		fail(err.Error())
	}
	defer func() { srv.Process.Kill(); srv.Wait() }()
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
		srv.Process.Kill()
		fail("smoke: the server never listened on -addr")
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
		}
		srv.Process.Kill()
		srv.Wait()
		os.Exit(1)
	}
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
