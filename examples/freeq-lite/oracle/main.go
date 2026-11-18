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

// drainUntil reads lines until want arrives (skipping others).
func (k *conn) drainUntil(want string) error {
	deadline := time.After(wait)
	for {
		select {
		case got, ok := <-k.lines:
			if !ok {
				return fmt.Errorf("connection closed, wanted %q", want)
			}
			if got == want {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("timed out waiting for %q", want)
		}
	}
}

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
		if err := ka.expect(":" + pfx(na) + " JOIN " + ch); err != nil {
			return err
		}
		if err := ka.expect(":freeq-lite 353 " + na + " = " + ch + " :" + na); err != nil {
			return err
		}
		if err := ka.expect(":freeq-lite 366 " + na + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		kb.send("JOIN " + ch)
		if err := ka.expect(":" + pfx(nb) + " JOIN " + ch); err != nil {
			return err
		}
		if err := kb.expect(":" + pfx(nb) + " JOIN " + ch); err != nil {
			return err
		}
		if err := kb.expect(":freeq-lite 353 " + nb + " = " + ch + " :" + na + " " + nb); err != nil {
			return err
		}
		if err := kb.expect(":freeq-lite 366 " + nb + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
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
		if err := k.expect(":" + pfx(n) + " JOIN " + ch); err != nil {
			return err
		}
		if err := k.expect(":freeq-lite 353 " + n + " = " + ch + " :" + n); err != nil {
			return err
		}
		if err := k.expect(":freeq-lite 366 " + n + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
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
		if err := k.expect(":" + pfx(n) + " JOIN " + ch); err != nil {
			return err
		}
		if err := k.expect(":freeq-lite 353 " + n + " = " + ch + " :" + n); err != nil {
			return err
		}
		if err := k.expect(":freeq-lite 366 " + n + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
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
		if err := k1.expect("ERROR :Closing link"); err != nil {
			return err
		}
		if err := k1.closed(); err != nil {
			return err
		}
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
		na, nb := uniq("kai"), uniq("lev")
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
		// Sent to a second user, so a server with no limit visibly delivers it.
		ka.send("PRIVMSG " + nb + " :" + strings.Repeat("x", 600))
		ka.send("PING still-here")
		return first(ka.expect(":freeq-lite PONG freeq-lite :still-here"), kb.silent(400*time.Millisecond))
	}},
	{"privmsg_to_self_not_delivered", func(a string) error {
		n := uniq("mae")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("PRIVMSG "+n+" :note to self", "PING after")
		return k.expect(":freeq-lite PONG freeq-lite :after")
	}},
	{"nick_change_revalidated_432", func(a string) error {
		n := uniq("neo")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("NICK 1bad")
		return k.expect(":freeq-lite 432 * 1bad :Erroneous nickname")
	}},
	{"channel_name_boundaries", func(a string) error {
		n := uniq("ora")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		ch := "#" + strings.Repeat("c", 26) + fmt.Sprintf("%05d", seq.Add(1)%100000) // exactly 32
		k.send("JOIN " + ch)
		if err := k.expect(":" + pfx(n) + " JOIN " + ch); err != nil {
			return fmt.Errorf("a 32-character channel was refused: %w", err)
		}
		if err := k.expect(":freeq-lite 353 " + n + " = " + ch + " :" + n); err != nil {
			return err
		}
		if err := k.expect(":freeq-lite 366 " + n + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		bad := "#bell\x07ring"
		k.send("JOIN " + bad)
		return k.expect(":freeq-lite 403 " + n + " " + bad + " :No such channel")
	}},
	{"privmsg_empty_text_412", func(a string) error {
		na, nb := uniq("pax"), uniq("quo")
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
		ka.send("PRIVMSG " + nb + " :")
		return first(ka.expect(":freeq-lite 412 "+na+" :No text to send"), kb.silent(300*time.Millisecond))
	}},
	{"unregistered_451_for_every_command", func(a string) error {
		k, err := dial(a)
		if err != nil {
			return err
		}
		defer k.close()
		for _, line := range []string{"PRIVMSG someone :hi", "PART #x", "NAMES #x", "WHATEVER"} {
			k.send(line)
			if err := k.expect(":freeq-lite 451 * :You have not registered"); err != nil {
				return fmt.Errorf("%s: %w", line, err)
			}
		}
		return nil
	}},
	{"nick_boundary_and_charset_accepted", func(a string) error {
		// '_' and '-' after the first letter are valid; so is exactly 16 characters.
		for _, n := range []string{fmt.Sprintf("a_b-c%d", seq.Add(1)), fmt.Sprintf("x-y_%d", seq.Add(1))} {
			k, err := register(a, n)
			if err != nil {
				return fmt.Errorf("valid nick %q: %w", n, err)
			}
			k.close()
		}
		n := fmt.Sprintf("p%015d", seq.Add(1)) // exactly 16, unique per check
		k, err := register(a, n)
		if err != nil {
			return fmt.Errorf("16-character nick %q: %w", n, err)
		}
		k.close()
		return nil
	}},
	{"old_nick_freed_after_change", func(a string) error {
		old, nn := uniq("lou"), uniq("lulu")
		ka, err := register(a, old)
		if err != nil {
			return err
		}
		defer ka.close()
		ka.send("NICK " + nn)
		if err := ka.expect(":" + pfx(old) + " NICK " + nn); err != nil {
			return err
		}
		kb, err := register(a, old) // the old nick is free again
		if err != nil {
			return err
		}
		kb.close()
		return nil
	}},
	{"nick_change_not_sent_to_strangers", func(a string) error {
		na, nc := uniq("moe"), uniq("nan")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		kc, err := register(a, nc) // shares no channel with na
		if err != nil {
			return err
		}
		defer kc.close()
		nn := uniq("moey")
		ka.send("NICK " + nn)
		return first(ka.expect(":"+pfx(na)+" NICK "+nn), kc.silent(400*time.Millisecond))
	}},
	{"dropped_user_leaves_channels", func(a string) error {
		ch := uniq("#left")
		na, nb := uniq("ola"), uniq("pia")
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
		ka.close()
		if err := kb.drainUntil(":" + pfx(na) + " QUIT :Connection closed"); err != nil {
			return err
		}
		kb.send("NAMES " + ch)
		return first(kb.expect(":freeq-lite 353 "+nb+" = "+ch+" :"+nb),
			kb.expect(":freeq-lite 366 "+nb+" "+ch+" :End of /NAMES list"))
	}},
	{"privmsg_nick_not_echoed", func(a string) error {
		na, nb := uniq("quinn"), uniq("rita")
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
		ka.send("PRIVMSG " + nb + " :just you")
		return first(kb.expect(":"+pfx(na)+" PRIVMSG "+nb+" :just you"), ka.silent(400*time.Millisecond))
	}},
	{"quit_not_sent_to_strangers", func(a string) error {
		na, nc := uniq("sol"), uniq("tom")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		kc, err := register(a, nc)
		if err != nil {
			return err
		}
		defer kc.close()
		ka.send("JOIN " + uniq("#alone"))
		time.Sleep(200 * time.Millisecond)
		ka.send("QUIT :bye")
		ka.close()
		return kc.silent(500 * time.Millisecond)
	}},
	{"channel_names_ignore_case", func(a string) error {
		base := uniq("#Room")
		na, nb := uniq("ali"), uniq("bea")
		ka, err := register(a, na)
		if err != nil {
			return err
		}
		defer ka.close()
		ka.send("JOIN " + base)
		if err := first(ka.expect(":"+pfx(na)+" JOIN "+base),
			ka.expect(":freeq-lite 353 "+na+" = "+base+" :"+na),
			ka.expect(":freeq-lite 366 "+na+" "+base+" :End of /NAMES list")); err != nil {
			return err
		}
		kb, err := register(a, nb)
		if err != nil {
			return err
		}
		defer kb.close()
		lower, upper := strings.ToLower(base), strings.ToUpper(base)
		kb.send("JOIN " + lower)
		// Same channel, shown with the name it was created with.
		if err := first(ka.expect(":"+pfx(nb)+" JOIN "+base), kb.expect(":"+pfx(nb)+" JOIN "+base),
			kb.expect(":freeq-lite 353 "+nb+" = "+base+" :"+na+" "+nb),
			kb.expect(":freeq-lite 366 "+nb+" "+base+" :End of /NAMES list")); err != nil {
			return err
		}
		// PRIVMSG under another case reaches the same channel.
		kb.send("PRIVMSG " + upper + " :hi all")
		if err := ka.expect(":" + pfx(nb) + " PRIVMSG " + base + " :hi all"); err != nil {
			return fmt.Errorf("PRIVMSG %s: %w", upper, err)
		}
		// NAMES under another case lists the same members.
		ka.send("NAMES " + upper)
		if err := first(ka.expect(":freeq-lite 353 "+na+" = "+base+" :"+na+" "+nb),
			ka.expect(":freeq-lite 366 "+na+" "+base+" :End of /NAMES list")); err != nil {
			return fmt.Errorf("NAMES %s: %w", upper, err)
		}
		// PART under another case leaves the same channel.
		kb.send("PART " + upper)
		if err := first(ka.expect(":"+pfx(nb)+" PART "+base), kb.expect(":"+pfx(nb)+" PART "+base)); err != nil {
			return fmt.Errorf("PART %s: %w", upper, err)
		}
		return nil
	}},
	{"part_reason_reaches_members", func(a string) error {
		ch := uniq("#bye")
		na, nb := uniq("cid"), uniq("dot")
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
		if err := ka.drainUntil(":freeq-lite 366 " + na + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		kb.send("JOIN " + ch)
		if err := ka.drainUntil(":" + pfx(nb) + " JOIN " + ch); err != nil {
			return err
		}
		kb.send("PART " + ch + " :see you")
		return ka.expect(":" + pfx(nb) + " PART " + ch + " :see you")
	}},
	{"quit_removes_from_channels", func(a string) error {
		ch := uniq("#stay")
		na, nb := uniq("eli"), uniq("fox")
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
		if err := ka.drainUntil(":freeq-lite 366 " + na + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		kb.send("JOIN " + ch)
		if err := kb.drainUntil(":freeq-lite 366 " + nb + " " + ch + " :End of /NAMES list"); err != nil {
			return err
		}
		ka.send("QUIT :done")
		if err := kb.drainUntil(":" + pfx(na) + " QUIT :done"); err != nil {
			return err
		}
		ka.close()
		kb.send("NAMES " + ch)
		return first(kb.expect(":freeq-lite 353 "+nb+" = "+ch+" :"+nb),
			kb.expect(":freeq-lite 366 "+nb+" "+ch+" :End of /NAMES list"))
	}},
	{"forgotten_channel_takes_new_name", func(a string) error {
		// A channel forgotten when its last member left (by PART, QUIT or a
		// dropped connection) is new when joined again, and takes the name it
		// is created with this time.
		for _, how := range []string{"part", "quit", "drop"} {
			name := uniq("#Phoenix")
			again := strings.ToUpper(name)
			n := uniq("gil")
			k, err := register(a, n)
			if err != nil {
				return err
			}
			k.send("JOIN " + name)
			if err := k.drainUntil(":freeq-lite 366 " + n + " " + name + " :End of /NAMES list"); err != nil {
				k.close()
				return err
			}
			switch how {
			case "part":
				k.send("PART " + name)
				if err := k.drainUntil(":" + pfx(n) + " PART " + name); err != nil {
					k.close()
					return err
				}
			case "quit":
				k.send("QUIT")
				k.closed()
			}
			k.close()
			n2 := uniq("hal")
			k2, err := register(a, n2)
			if err != nil {
				return err
			}
			time.Sleep(100 * time.Millisecond) // let the server process the drop
			k2.send("JOIN " + again)
			err = first(k2.expect(":"+pfx(n2)+" JOIN "+again), k2.expect(":freeq-lite 353 "+n2+" = "+again+" :"+n2))
			k2.close()
			if err != nil {
				return fmt.Errorf("after %s: %w", how, err)
			}
		}
		return nil
	}},
}

// v2Checks are the Topics revision of the spec (spec-v2/), run with -v2.
var v2Checks = []check{
	{"topic_set_broadcast_and_query", func(a string) error {
		ch := uniq("#topic")
		na, nb := uniq("una"), uniq("vik")
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
		kb.send("TOPIC " + ch)
		if err := kb.expect(":freeq-lite 331 " + nb + " " + ch + " :No topic is set"); err != nil {
			return err
		}
		ka.send("TOPIC " + ch + " :welcome all")
		msg := ":" + pfx(na) + " TOPIC " + ch + " :welcome all"
		if err := first(ka.expect(msg), kb.expect(msg)); err != nil {
			return err
		}
		kb.send("TOPIC " + ch)
		return kb.expect(":freeq-lite 332 " + nb + " " + ch + " :welcome all")
	}},
	{"topic_non_member_442", func(a string) error {
		ch := uniq("#closed")
		na, nb := uniq("wyn"), uniq("xan")
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
		kb.send("TOPIC " + ch + " :mine now")
		return kb.expect(":freeq-lite 442 " + nb + " " + ch + " :You're not on that channel")
	}},
	{"topic_unknown_channel_403", func(a string) error {
		n := uniq("yul")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		ch := uniq("#nowhere")
		k.send("TOPIC " + ch)
		return k.expect(":freeq-lite 403 " + n + " " + ch + " :No such channel")
	}},
	{"topic_forgotten_with_channel", func(a string) error {
		ch := uniq("#fleeting")
		n := uniq("zia")
		k, err := register(a, n)
		if err != nil {
			return err
		}
		defer k.close()
		k.send("JOIN "+ch, "TOPIC "+ch+" :brief", "PART "+ch, "JOIN "+ch)
		time.Sleep(300 * time.Millisecond)
		for len(k.lines) > 0 {
			<-k.lines
		}
		k.send("TOPIC " + ch)
		return k.expect(":freeq-lite 331 " + n + " " + ch + " :No topic is set")
	}},
}

// start launches bin on a free port and waits until it accepts connections.
func start(bin string, extra ...string) (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(bin, append([]string{"-addr", addr}, extra...)...)
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
	withV2 := flag.Bool("v2", false, "include the checks for the Topics spec revision")
	flag.Parse()
	if (*bin == "") == (*addr == "") {
		fmt.Fprintln(os.Stderr, "oracle: exactly one of -bin or -addr")
		os.Exit(2)
	}
	r := result{Failed: []string{}, Errors: map[string]string{}}
	all := checks
	if *withV2 {
		all = append(append([]check{}, checks...), v2Checks...)
	}
	for _, c := range all {
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
