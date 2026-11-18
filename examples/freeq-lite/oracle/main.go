// Command oracle is the freeq-lite behaviour suite: the yardstick every
// generated server is measured against. It drives a server over TCP and
// imports nothing from any implementation. It was written from the spec
// before any code was generated, and validated against a hand-written
// reference server, which it must score 100%.
//
//	oracle -bin ./server        # starts a fresh server per check
//	oracle -addr 127.0.0.1:6667 # an already-running server (checks use unique names)
//
// It prints one JSON object: {"checks":N,"passed":M,"failed":[...]} and exits
// 0 when every check passes, 1 otherwise, 2 when the server cannot be reached.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

const wait = 2 * time.Second

type conn struct {
	c     net.Conn
	lines chan string
}

func dial(addr string) (*conn, error) {
	c, err := net.DialTimeout("tcp", addr, wait)
	if err != nil {
		return nil, err
	}
	k := &conn{c: c, lines: make(chan string, 1024)}
	go func() {
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			k.lines <- strings.TrimRight(sc.Text(), "\r")
		}
		close(k.lines)
	}()
	return k, nil
}

func (k *conn) send(lines ...string) {
	for _, l := range lines {
		k.c.Write([]byte(l + "\r\n"))
	}
}

func (k *conn) raw(s string) { k.c.Write([]byte(s)) }

// expect waits for the exact line, skipping nothing: the next line must be it.
func (k *conn) expect(want string) error {
	select {
	case got, ok := <-k.lines:
		if !ok {
			return fmt.Errorf("connection closed, wanted %q", want)
		}
		if got != want {
			return fmt.Errorf("got %q, want %q", got, want)
		}
		return nil
	case <-time.After(wait):
		return fmt.Errorf("timed out waiting for %q", want)
	}
}

// silent asserts that no line arrives for d.
func (k *conn) silent(d time.Duration) error {
	select {
	case got, ok := <-k.lines:
		if !ok {
			return errors.New("connection closed unexpectedly")
		}
		return fmt.Errorf("unexpected line %q", got)
	case <-time.After(d):
		return nil
	}
}

// closed asserts the server closes the connection.
func (k *conn) closed() error {
	select {
	case got, ok := <-k.lines:
		if ok {
			return fmt.Errorf("expected close, got %q", got)
		}
		return nil
	case <-time.After(wait):
		return errors.New("connection not closed")
	}
}

func (k *conn) close() { k.c.Close() }

var seq atomic.Int64

// uniq makes nick and channel names unique across checks on a shared server.
func uniq(base string) string { return fmt.Sprintf("%s%d", base, seq.Add(1)) }

// register connects and registers nick, consuming the 001.
func register(addr, nick string) (*conn, error) {
	k, err := dial(addr)
	if err != nil {
		return nil, err
	}
	k.send("NICK "+nick, "USER "+strings.ToLower(nick)+" 0 * :Real "+nick)
	if err := k.expect(":freeq-lite 001 " + nick + " :Welcome to freeq-lite " + nick); err != nil {
		k.close()
		return nil, err
	}
	return k, nil
}

func pfx(nick string) string { return nick + "!" + strings.ToLower(nick) + "@freeq-lite" }

func first(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

type check struct {
	name string
	run  func(addr string) error
}

var checks = []check{
	{"register_nick_then_user", func(a string) error {
		n := uniq("ann")
		k, err := register(a, n)
		if err == nil {
			k.close()
		}
		return err
	}},
	{"register_user_then_nick", func(a string) error {
		n := uniq("bob")
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("USER bob 0 * :Bob", "NICK "+n)
		return k.expect(":freeq-lite 001 " + n + " :Welcome to freeq-lite " + n)
	}},
	{"commands_case_insensitive", func(a string) error {
		n := uniq("cat")
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("nick "+n, "User cat 0 * :Cat")
		return k.expect(":freeq-lite 001 " + n + " :Welcome to freeq-lite " + n)
	}},
	{"bare_lf_line_endings", func(a string) error {
		n := uniq("dan")
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		k.raw("NICK " + n + "\nUSER dan 0 * :Dan\n")
		return k.expect(":freeq-lite 001 " + n + " :Welcome to freeq-lite " + n)
	}},
	{"invalid_nick_432", func(a string) error {
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		for _, bad := range []string{"9lives", "bad!nick", "abcdefghijklmnopq"} {
			k.send("NICK " + bad)
			if err := k.expect(":freeq-lite 432 * " + bad + " :Erroneous nickname"); err != nil {
				return err
			}
		}
		return nil
	}},
	{"nick_in_use_433_ignoring_case", func(a string) error {
		n := uniq("eve")
		k1, err := register(a, n)
		if err != nil {
			return err
		}
		defer k1.close()
		k2, err := dial(a)
		if err != nil {
			return err
		}
		defer k2.close()
		up := strings.ToUpper(n)
		k2.send("NICK " + up)
		return k2.expect(":freeq-lite 433 * " + up + " :Nickname is already in use")
	}},
	{"unregistered_451", func(a string) error {
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("JOIN #x")
		return k.expect(":freeq-lite 451 * :You have not registered")
	}},
	{"ping_before_registration", func(a string) error {
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("PING tok123")
		return k.expect(":freeq-lite PONG freeq-lite :tok123")
	}},
	{"ping_after_registration", func(a string) error {
		k, err := register(a, uniq("fay"))
		if err != nil {
			return err
		}
		defer k.close()
		k.send("PING abc")
		return k.expect(":freeq-lite PONG freeq-lite :abc")
	}},
	{"join_broadcast_and_names", func(a string) error {
		ch := uniq("#room")
		na, nb := uniq("amy"), uniq("ben")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		ka.send("JOIN " + ch)
		if err := first(ka.expect(":"+pfx(na)+" JOIN "+ch),
			ka.expect(":freeq-lite 353 "+na+" = "+ch+" :"+na),
			ka.expect(":freeq-lite 366 "+na+" "+ch+" :End of /NAMES list")); err != nil {
			return err
		}
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		kb.send("JOIN " + ch)
		return first(
			ka.expect(":"+pfx(nb)+" JOIN "+ch),
			kb.expect(":"+pfx(nb)+" JOIN "+ch),
			kb.expect(":freeq-lite 353 "+nb+" = "+ch+" :"+na+" "+nb),
			kb.expect(":freeq-lite 366 "+nb+" "+ch+" :End of /NAMES list"))
	}},
	{"names_sorted_alphabetically", func(a string) error {
		ch := uniq("#sort")
		nz, nm, na := uniq("zed"), uniq("max"), uniq("abe")
		var ks []*conn
		for _, n := range []string{nz, nm, na} {
			k, err := register(a, n)
			if err != nil {
				return err
			}
			defer k.close()
			k.send("JOIN " + ch)
			ks = append(ks, k)
		}
		// drain joins/names, then ask explicitly
		time.Sleep(300 * time.Millisecond)
		k := ks[2]
		for len(k.lines) > 0 {
			<-k.lines
		}
		k.send("NAMES " + ch)
		return first(k.expect(":freeq-lite 353 "+na+" = "+ch+" :"+na+" "+nm+" "+nz),
			k.expect(":freeq-lite 366 "+na+" "+ch+" :End of /NAMES list"))
	}},
	{"join_twice_is_silent", func(a string) error {
		ch := uniq("#twice")
		n := uniq("gus")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("JOIN " + ch)
		if err := first(k.expect(":"+pfx(n)+" JOIN "+ch), k.expect(":freeq-lite 353 "+n+" = "+ch+" :"+n),
			k.expect(":freeq-lite 366 "+n+" "+ch+" :End of /NAMES list")); err != nil {
			return err
		}
		k.send("JOIN " + ch)
		return k.silent(500 * time.Millisecond)
	}},
	{"invalid_channel_403", func(a string) error {
		n := uniq("hal")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		for _, bad := range []string{"nohash", "#" + strings.Repeat("x", 32), "#a,b", "#"} {
			k.send("JOIN " + bad)
			if err := k.expect(":freeq-lite 403 " + n + " " + bad + " :No such channel"); err != nil {
				return err
			}
		}
		return nil
	}},
	{"part_broadcast", func(a string) error {
		ch := uniq("#part")
		na, nb := uniq("ida"), uniq("jon")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("JOIN " + ch)
		ka.expect(":" + pfx(na) + " JOIN " + ch)
		ka.expect(":freeq-lite 353 " + na + " = " + ch + " :" + na)
		ka.expect(":freeq-lite 366 " + na + " " + ch + " :End of /NAMES list")
		kb.send("JOIN " + ch)
		ka.expect(":" + pfx(nb) + " JOIN " + ch)
		kb.expect(":" + pfx(nb) + " JOIN " + ch)
		kb.expect(":freeq-lite 353 " + nb + " = " + ch + " :" + na + " " + nb)
		kb.expect(":freeq-lite 366 " + nb + " " + ch + " :End of /NAMES list")
		kb.send("PART " + ch)
		return first(ka.expect(":"+pfx(nb)+" PART "+ch), kb.expect(":"+pfx(nb)+" PART "+ch))
	}},
	{"part_with_reason", func(a string) error {
		ch := uniq("#why")
		n := uniq("kim")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("JOIN " + ch)
		k.expect(":" + pfx(n) + " JOIN " + ch)
		k.expect(":freeq-lite 353 " + n + " = " + ch + " :" + n)
		k.expect(":freeq-lite 366 " + n + " " + ch + " :End of /NAMES list")
		k.send("PART " + ch + " :gone fishing")
		return k.expect(":" + pfx(n) + " PART " + ch + " :gone fishing")
	}},
	{"part_not_member_442", func(a string) error {
		n := uniq("lee")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		ch := uniq("#nope")
		k.send("PART " + ch)
		return k.expect(":freeq-lite 442 " + n + " " + ch + " :You're not on that channel")
	}},
	{"empty_channel_is_forgotten", func(a string) error {
		ch := uniq("#gone")
		n := uniq("mia")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("JOIN " + ch)
		k.expect(":" + pfx(n) + " JOIN " + ch)
		k.expect(":freeq-lite 353 " + n + " = " + ch + " :" + n)
		k.expect(":freeq-lite 366 " + n + " " + ch + " :End of /NAMES list")
		k.send("PART "+ch, "NAMES "+ch)
		return first(k.expect(":"+pfx(n)+" PART "+ch), k.expect(":freeq-lite 366 "+n+" "+ch+" :End of /NAMES list"))
	}},
	{"names_unknown_channel_only_366", func(a string) error {
		n := uniq("ned")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		ch := uniq("#none")
		k.send("NAMES " + ch)
		return first(k.expect(":freeq-lite 366 "+n+" "+ch+" :End of /NAMES list"), k.silent(300*time.Millisecond))
	}},
	{"privmsg_channel_excludes_sender", func(a string) error {
		ch := uniq("#talk")
		na, nb := uniq("oli"), uniq("pat")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("JOIN " + ch)
		kb.send("JOIN " + ch)
		time.Sleep(300 * time.Millisecond)
		for len(ka.lines) > 0 {
			<-ka.lines
		}
		for len(kb.lines) > 0 {
			<-kb.lines
		}
		ka.send("PRIVMSG " + ch + " :hello there")
		return first(kb.expect(":"+pfx(na)+" PRIVMSG "+ch+" :hello there"), ka.silent(400*time.Millisecond))
	}},
	{"privmsg_channel_non_member_404", func(a string) error {
		ch := uniq("#club")
		na, nb := uniq("quin"), uniq("rae")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		ka.send("JOIN " + ch)
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		time.Sleep(200 * time.Millisecond)
		kb.send("PRIVMSG " + ch + " :let me in")
		return kb.expect(":freeq-lite 404 " + nb + " " + ch + " :Cannot send to channel")
	}},
	{"privmsg_nick_ignoring_case", func(a string) error {
		na, nb := uniq("sam"), uniq("tia")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("PRIVMSG " + strings.ToUpper(nb) + " :psst")
		return kb.expect(":" + pfx(na) + " PRIVMSG " + nb + " :psst")
	}},
	{"privmsg_unknown_target_401", func(a string) error {
		n := uniq("uma")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		ghost, room := uniq("ghost"), uniq("#void")
		k.send("PRIVMSG "+ghost+" :hi", "PRIVMSG "+room+" :hi")
		return first(k.expect(":freeq-lite 401 "+n+" "+ghost+" :No such nick/channel"),
			k.expect(":freeq-lite 401 "+n+" "+room+" :No such nick/channel"))
	}},
	{"privmsg_no_text_412", func(a string) error {
		na, nb := uniq("vic"), uniq("wes")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("PRIVMSG " + nb)
		return ka.expect(":freeq-lite 412 " + na + " :No text to send")
	}},
	{"quit_sends_error_and_closes", func(a string) error {
		k, err := register(a, uniq("xia"))
		if err != nil {
			return err
		}
		defer k.close()
		k.send("QUIT")
		return first(k.expect("ERROR :Closing link"), k.closed())
	}},
	{"quit_broadcast_once_with_reason", func(a string) error {
		c1, c2 := uniq("#one"), uniq("#two")
		na, nb := uniq("yan"), uniq("zoe")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("JOIN "+c1, "JOIN "+c2)
		kb.send("JOIN "+c1, "JOIN "+c2)
		time.Sleep(300 * time.Millisecond)
		for len(kb.lines) > 0 {
			<-kb.lines
		}
		ka.send("QUIT :bye now")
		return first(kb.expect(":"+pfx(na)+" QUIT :bye now"), kb.silent(400*time.Millisecond))
	}},
	{"quit_default_reason", func(a string) error {
		ch := uniq("#dflt")
		na, nb := uniq("abby"), uniq("bert")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("JOIN " + ch)
		kb.send("JOIN " + ch)
		time.Sleep(300 * time.Millisecond)
		for len(kb.lines) > 0 {
			<-kb.lines
		}
		ka.send("QUIT")
		return kb.expect(":" + pfx(na) + " QUIT :Client quit")
	}},
	{"dropped_connection_reason", func(a string) error {
		ch := uniq("#drop")
		na, nb := uniq("cole"), uniq("dina")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("JOIN " + ch)
		kb.send("JOIN " + ch)
		time.Sleep(300 * time.Millisecond)
		for len(kb.lines) > 0 {
			<-kb.lines
		}
		ka.close()
		return kb.expect(":" + pfx(na) + " QUIT :Connection closed")
	}},
	{"nick_free_after_quit", func(a string) error {
		n := uniq("emil")
		k1, err := register(a, n)
		if err != nil {
			return err
		}
		k1.send("QUIT")
		k1.expect("ERROR :Closing link")
		k1.closed()
		k1.close()
		time.Sleep(200 * time.Millisecond)
		k2, err := register(a, n)
		if err == nil {
			k2.close()
		}
		return err
	}},
	{"nick_change_broadcast_once", func(a string) error {
		c1, c2 := uniq("#n1"), uniq("#n2")
		na, nb := uniq("finn"), uniq("gwen")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("JOIN "+c1, "JOIN "+c2)
		kb.send("JOIN "+c1, "JOIN "+c2)
		time.Sleep(300 * time.Millisecond)
		for len(ka.lines) > 0 {
			<-ka.lines
		}
		for len(kb.lines) > 0 {
			<-kb.lines
		}
		nn := uniq("finny")
		ka.send("NICK " + nn)
		msg := ":" + pfx(na) + " NICK " + nn
		return first(ka.expect(msg), kb.expect(msg), kb.silent(400*time.Millisecond))
	}},
	{"nick_change_to_taken_433", func(a string) error {
		na, nb := uniq("hugo"), uniq("iris")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		ka.send("NICK " + nb)
		return ka.expect(":freeq-lite 433 * " + nb + " :Nickname is already in use")
	}},
	{"unknown_command_421_as_sent", func(a string) error {
		n := uniq("jade")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("FrObNiCaTe now")
		return k.expect(":freeq-lite 421 " + n + " FrObNiCaTe :Unknown command")
	}},
	{"overlong_line_ignored", func(a string) error {
		n := uniq("kai")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("PRIVMSG " + n + " :" + strings.Repeat("x", 600))
		k.send("PING still-here")
		return k.expect(":freeq-lite PONG freeq-lite :still-here")
	}},
}

// start launches bin on a free port and waits until it accepts connections.
func start(bin string) (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(bin, "-addr", addr)
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	stop := func() { cmd.Process.Kill(); cmd.Wait() }
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return addr, stop, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	return "", nil, fmt.Errorf("server did not listen on %s", addr)
}

type result struct {
	Checks int               `json:"checks"`
	Passed int               `json:"passed"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors,omitempty"`
}

func main() {
	bin := flag.String("bin", "", "server binary to start once per check")
	addr := flag.String("addr", "", "address of an already-running server")
	only := flag.String("run", "", "run only checks whose name contains this")
	flag.Parse()
	if (*bin == "") == (*addr == "") {
		fmt.Fprintln(os.Stderr, "oracle: exactly one of -bin or -addr")
		os.Exit(2)
	}
	r := result{Failed: []string{}, Errors: map[string]string{}}
	for _, c := range checks {
		if *only != "" && !strings.Contains(c.name, *only) {
			continue
		}
		a := *addr
		stop := func() {}
		if *bin != "" {
			var err error
			a, stop, err = start(*bin)
			if err != nil {
				fmt.Fprintln(os.Stderr, "oracle:", err)
				os.Exit(2)
			}
		}
		r.Checks++
		if err := c.run(a); err != nil {
			r.Failed = append(r.Failed, c.name)
			r.Errors[c.name] = err.Error()
		} else {
			r.Passed++
		}
		stop()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(r)
	if r.Passed != r.Checks {
		os.Exit(1)
	}
}
