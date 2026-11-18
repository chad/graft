package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mutant is a deliberate bug planted in a copy of the reference. The oracle
// must fail the named check on it; that is what makes "the oracle has teeth"
// a claim anyone can re-run rather than an anecdote.
type mutant struct {
	name, from, to string
	mustFail       string
}

var mutants = []mutant{
	// The six from the PR description.
	{"unsorted_names", "sort.Strings(ns)", "_ = sort.Strings", "names_sorted_alphabetically"},
	{"channel_echo_to_sender", "if m != c {\n\t\t\t\t\th.send(m, msg)", "if true {\n\t\t\t\t\th.send(m, msg)", "privmsg_channel_excludes_sender"},
	{"quit_per_channel", "for p := range h.peers(c) {\n\t\t\th.send(p, \":\"+c.prefix()+\" QUIT :\"+reason)\n\t\t}",
		"for ch := range c.channels {\n\t\t\tfor m := range h.channels[ch] {\n\t\t\t\tif m != c {\n\t\t\t\t\th.send(m, \":\"+c.prefix()+\" QUIT :\"+reason)\n\t\t\t\t}\n\t\t\t}\n\t\t}",
		"quit_broadcast_once_with_reason"},
	{"no_nick_validation", "if !nickRE.MatchString(n) {", "if false && !nickRE.MatchString(n) {", "invalid_nick_432"},
	{"case_sensitive_nicks", "if o, ok := h.nicks[strings.ToLower(n)]; ok && o != c {", "if o, ok := h.nicks[n]; ok && o != c {", "nick_in_use_433_ignoring_case"},
	{"no_line_limit", "if len(line) > 510 {", "if false {", "overlong_line_ignored"},
	// Bugs livecodelife's agent wrote against the first oracle, which escaped it.
	{"sixteen_char_nick_rejected", "[A-Za-z0-9_-]{0,15}$", "[A-Za-z0-9_-]{0,14}$", "nick_boundary_and_charset_accepted"},
	{"underscore_dash_rejected", "[A-Za-z0-9_-]{0,15}$", "[A-Za-z0-9]{0,15}$", "nick_boundary_and_charset_accepted"},
	{"old_nick_not_freed", "old := c.prefix()\n\t\t\tdelete(h.nicks, strings.ToLower(c.nick))", "old := c.prefix()", "old_nick_freed_after_change"},
	{"dropped_user_stays_in_channels", "for ch := range c.channels {\n\t\tdelete(h.channels[ch], c)", "for ch := range c.channels {\n\t\tif false {\n\t\t\tdelete(h.channels[ch], c)\n\t\t}", "dropped_user_leaves_channels"},
	{"nick_change_to_everyone", "for peer := range h.peers(c) {\n\t\t\t\th.send(peer, msg)", "for _, peer := range h.nicks {\n\t\t\t\th.send(peer, msg)", "nick_change_not_sent_to_strangers"},
	{"private_message_echoed", "h.send(to, \":\"+c.prefix()+\" PRIVMSG \"+to.nick+\" :\"+text)", "h.send(to, \":\"+c.prefix()+\" PRIVMSG \"+to.nick+\" :\"+text)\n\t\th.send(c, \":\"+c.prefix()+\" PRIVMSG \"+to.nick+\" :\"+text)", "privmsg_nick_not_echoed"},
	{"quit_to_everyone", "for p := range h.peers(c) {\n\t\t\th.send(p, \":\"+c.prefix()+\" QUIT :\"+reason)", "for _, p := range h.nicks {\n\t\t\th.send(p, \":\"+c.prefix()+\" QUIT :\"+reason)", "quit_not_sent_to_strangers"},
	{"delivered_nick_as_typed", "h.send(to, \":\"+c.prefix()+\" PRIVMSG \"+to.nick+\" :\"+text)", "h.send(to, \":\"+c.prefix()+\" PRIVMSG \"+target+\" :\"+text)", "privmsg_nick_ignoring_case"},
}

func TestMutantsAreCaught(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	src, err := os.ReadFile("../reference/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutants {
		t.Run(m.name, func(t *testing.T) {
			if !strings.Contains(string(src), m.from) {
				t.Fatalf("mutation anchor not found in the reference: %q", m.from)
			}
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module mutant\n\ngo 1.22\n"), 0o644)
			os.WriteFile(filepath.Join(dir, "main.go"), []byte(strings.Replace(string(src), m.from, m.to, 1)), 0o644)
			bin := filepath.Join(dir, "server")
			cmd := exec.Command("go", "build", "-o", bin, ".")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("mutant does not build: %v\n%s", err, out)
			}
			var target *check
			for i := range checks {
				if checks[i].name == m.mustFail {
					target = &checks[i]
				}
			}
			if target == nil {
				t.Fatalf("no check named %q", m.mustFail)
			}
			addr, stop, err := start(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := target.run(addr); err == nil {
				t.Fatalf("check %s passed on mutant %s: the oracle missed it", m.mustFail, m.name)
			}
		})
	}
}
