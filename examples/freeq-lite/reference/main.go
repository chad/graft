// Command reference is a hand-written freeq-lite server. It exists only to
// validate the oracle: the oracle must score it 100%, or the oracle is wrong.
// It is never an input to graft and never shown to a model.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const server = "freeq-lite"

var (
	nickRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,15}$`)
)

func validChannel(c string) bool {
	if len(c) < 2 || len(c) > 32 || c[0] != '#' {
		return false
	}
	for _, r := range c {
		if r == ' ' || r == ',' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

type client struct {
	conn       net.Conn
	out        chan string
	nick, user string
	registered bool
	channels   map[string]bool // lower-cased channel name -> member
}

func (c *client) prefix() string { return c.nick + "!" + c.user + "@" + server }

type hub struct {
	mu       sync.Mutex
	nicks    map[string]*client          // lower-cased nick
	channels map[string]map[*client]bool // channel name (as created) keyed lower-case
	display  map[string]string           // lower-case -> display name
}

func (h *hub) send(c *client, line string) {
	select {
	case c.out <- line:
	default: // a client that cannot keep up loses lines rather than stalling everyone
	}
}

func (h *hub) reply(c *client, code, rest string) {
	target := c.nick
	if !c.registered {
		target = "*"
	}
	h.send(c, fmt.Sprintf(":%s %s %s %s", server, code, target, rest))
}

// peers is everyone sharing a channel with c, excluding c.
func (h *hub) peers(c *client) map[*client]bool {
	ps := map[*client]bool{}
	for ch := range c.channels {
		for m := range h.channels[ch] {
			if m != c {
				ps[m] = true
			}
		}
	}
	return ps
}

func (h *hub) sendNames(c *client, key string) {
	disp := key
	if d, ok := h.display[key]; ok {
		disp = d
	}
	if members, ok := h.channels[key]; ok {
		var ns []string
		for m := range members {
			ns = append(ns, m.nick)
		}
		sort.Strings(ns)
		h.reply(c, "353", "= "+disp+" :"+strings.Join(ns, " "))
	}
	h.reply(c, "366", disp+" :End of /NAMES list")
}

func (h *hub) leave(c *client, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.registered {
		for p := range h.peers(c) {
			h.send(p, ":"+c.prefix()+" QUIT :"+reason)
		}
		delete(h.nicks, strings.ToLower(c.nick))
	}
	for ch := range c.channels {
		delete(h.channels[ch], c)
		if len(h.channels[ch]) == 0 {
			delete(h.channels, ch)
			delete(h.display, ch)
		}
	}
	c.channels = map[string]bool{}
}

func parse(line string) (cmd string, params []string) {
	line = strings.TrimLeft(line, " ")
	if strings.HasPrefix(line, ":") { // a client-sent prefix is ignored
		if i := strings.IndexByte(line, ' '); i >= 0 {
			line = strings.TrimLeft(line[i:], " ")
		} else {
			return "", nil
		}
	}
	trailing, hasTrailing := "", false
	if i := strings.Index(line, " :"); i >= 0 {
		trailing, hasTrailing = line[i+2:], true
		line = line[:i]
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return "", nil
	}
	params = f[1:]
	if hasTrailing {
		params = append(params, trailing)
	}
	return f[0], params
}

// handle returns false when the connection should close.
func (h *hub) handle(c *client, line string) bool {
	raw, p := parse(line)
	if raw == "" {
		return true
	}
	cmd := strings.ToUpper(raw)
	h.mu.Lock()
	defer h.mu.Unlock()
	switch cmd {
	case "PING":
		tok := ""
		if len(p) > 0 {
			tok = p[0]
		}
		h.send(c, fmt.Sprintf(":%s PONG %s :%s", server, server, tok))
		return true
	case "QUIT":
		reason := "Client quit"
		if len(p) > 0 && p[0] != "" {
			reason = p[0]
		}
		h.send(c, "ERROR :Closing link")
		h.mu.Unlock()
		h.leave(c, reason)
		h.mu.Lock()
		return false
	case "NICK":
		if len(p) == 0 {
			return true
		}
		n := p[0]
		if !nickRE.MatchString(n) {
			h.send(c, fmt.Sprintf(":%s 432 * %s :Erroneous nickname", server, n))
			return true
		}
		if o, ok := h.nicks[strings.ToLower(n)]; ok && o != c {
			h.send(c, fmt.Sprintf(":%s 433 * %s :Nickname is already in use", server, n))
			return true
		}
		if c.registered {
			old := c.prefix()
			delete(h.nicks, strings.ToLower(c.nick))
			c.nick = n
			h.nicks[strings.ToLower(n)] = c
			msg := ":" + old + " NICK " + n
			h.send(c, msg)
			for peer := range h.peers(c) {
				h.send(peer, msg)
			}
			return true
		}
		if c.nick != "" {
			delete(h.nicks, strings.ToLower(c.nick))
		}
		c.nick = n
		h.nicks[strings.ToLower(n)] = c
		h.maybeRegister(c)
		return true
	case "USER":
		if c.registered || len(p) < 1 {
			return true
		}
		c.user = p[0]
		h.maybeRegister(c)
		return true
	}
	if !c.registered {
		h.send(c, fmt.Sprintf(":%s 451 * :You have not registered", server))
		return true
	}
	switch cmd {
	case "JOIN":
		if len(p) == 0 {
			return true
		}
		ch := p[0]
		if !validChannel(ch) {
			h.reply(c, "403", ch+" :No such channel")
			return true
		}
		key := strings.ToLower(ch)
		if c.channels[key] {
			return true
		}
		if h.channels[key] == nil {
			h.channels[key] = map[*client]bool{}
			h.display[key] = ch
		}
		h.channels[key][c] = true
		c.channels[key] = true
		msg := ":" + c.prefix() + " JOIN " + h.display[key]
		for m := range h.channels[key] {
			h.send(m, msg)
		}
		h.sendNames(c, key)
	case "PART":
		if len(p) == 0 {
			return true
		}
		key := strings.ToLower(p[0])
		if !c.channels[key] {
			h.reply(c, "442", p[0]+" :You're not on that channel")
			return true
		}
		msg := ":" + c.prefix() + " PART " + h.display[key]
		if len(p) > 1 {
			msg += " :" + p[1]
		}
		for m := range h.channels[key] {
			h.send(m, msg)
		}
		delete(h.channels[key], c)
		delete(c.channels, key)
		if len(h.channels[key]) == 0 {
			delete(h.channels, key)
			delete(h.display, key)
		}
	case "NAMES":
		if len(p) == 0 {
			return true
		}
		h.sendNames(c, strings.ToLower(p[0]))
	case "PRIVMSG":
		if len(p) < 2 || p[1] == "" {
			h.reply(c, "412", ":No text to send")
			return true
		}
		target, text := p[0], p[1]
		if strings.HasPrefix(target, "#") {
			key := strings.ToLower(target)
			members, ok := h.channels[key]
			if !ok {
				h.reply(c, "401", target+" :No such nick/channel")
				return true
			}
			if !c.channels[key] {
				h.reply(c, "404", target+" :Cannot send to channel")
				return true
			}
			msg := ":" + c.prefix() + " PRIVMSG " + h.display[key] + " :" + text
			for m := range members {
				if m != c {
					h.send(m, msg)
				}
			}
			return true
		}
		to, ok := h.nicks[strings.ToLower(target)]
		if !ok || !to.registered {
			h.reply(c, "401", target+" :No such nick/channel")
			return true
		}
		h.send(to, ":"+c.prefix()+" PRIVMSG "+to.nick+" :"+text)
	default:
		h.reply(c, "421", raw+" :Unknown command")
	}
	return true
}

func (h *hub) maybeRegister(c *client) {
	if c.registered || c.nick == "" || c.user == "" {
		return
	}
	c.registered = true
	h.send(c, fmt.Sprintf(":%s 001 %s :Welcome to freeq-lite %s", server, c.nick, c.nick))
}

func (h *hub) serve(conn net.Conn) {
	c := &client{conn: conn, out: make(chan string, 256), channels: map[string]bool{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := bufio.NewWriter(conn)
		for line := range c.out {
			w.WriteString(line + "\r\n")
			if len(c.out) == 0 {
				w.Flush()
			}
		}
		w.Flush()
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	quit := false
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if len(line) > 510 {
			continue
		}
		if !h.handle(c, line) {
			quit = true
			break
		}
	}
	if !quit {
		h.leave(c, "Connection closed")
	}
	close(c.out)
	<-done
	conn.Close()
}

func main() {
	addr := flag.String("addr", ":6667", "listen address")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	h := &hub{nicks: map[string]*client{}, channels: map[string]map[*client]bool{}, display: map[string]string{}}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go h.serve(conn)
	}
}
