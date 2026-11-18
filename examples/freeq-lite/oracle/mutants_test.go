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

// mutantAlso holds further from/to replacements for mutants whose bug spans
// two places.
var mutantAlso = map[string][][2]string{
	"display_name_survives_quit":       {{"h.display[key] = ch", "if _, ok := h.display[key]; !ok {\n\t\t\t\th.display[key] = ch\n\t\t\t}"}},
	"display_name_survives_forgetting": {{"h.display[key] = ch", "if _, ok := h.display[key]; !ok {\n\t\t\t\th.display[key] = ch\n\t\t\t}"}},
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
	{"privmsg_to_self_delivered", "if to == c {\n\t\t\treturn true // the sender never receives their own message back\n\t\t}", "", "privmsg_to_self_not_delivered"},
	{"no_nick_revalidation", "if !nickRE.MatchString(n) {", "if !c.registered && !nickRE.MatchString(n) {", "nick_change_revalidated_432"},
	{"channel_limit_31", "len(c) > 32", "len(c) > 31", "channel_name_boundaries"},
	{"control_chars_allowed", "r < 0x20 || ", "", "channel_name_boundaries"},
	{"empty_text_delivered", "if len(p) < 2 || p[1] == \"\" {", "if len(p) < 2 {", "privmsg_empty_text_412"},
	{"451_only_for_join", "\tif !c.registered {\n\t\th.send(c, fmt.Sprintf(\":%s 451 * :You have not registered\", server))", "\tif !c.registered && cmd == \"JOIN\" {\n\t\th.send(c, fmt.Sprintf(\":%s 451 * :You have not registered\", server))", "unregistered_451_for_every_command"},
	{"no_line_limit_to_peer", "if len(line) > 510 {", "if false {", "overlong_line_ignored"},
	{"topic_kept_after_channel_forgotten", "delete(h.display, key)\n\t\t\tdelete(h.topics, key)", "delete(h.display, key)", "topic_forgotten_with_channel"},
	{"channel_case_sensitive", "key := strings.ToLower(ch)\n\t\tif c.channels[key] {", "key := ch\n\t\tif c.channels[key] {", "channel_names_ignore_case"},
	{"part_case_sensitive", "key := strings.ToLower(p[0])\n\t\tif !c.channels[key] {\n\t\t\th.reply(c, \"442\"", "key := p[0]\n\t\tif !c.channels[key] {\n\t\t\th.reply(c, \"442\"", "channel_names_ignore_case"},
	{"privmsg_channel_case_sensitive", "key := strings.ToLower(target)", "key := target", "channel_names_ignore_case"},
	{"names_case_sensitive", "h.sendNames(c, strings.ToLower(p[0]))", "h.sendNames(c, p[0])", "channel_names_ignore_case"},
	{"display_name_survives_forgetting", "delete(h.channels, key)\n\t\t\tdelete(h.display, key)", "delete(h.channels, key)", "forgotten_channel_takes_new_name"},
	{"display_name_survives_quit", "delete(h.channels, ch)\n\t\t\tdelete(h.display, ch)", "delete(h.channels, ch)", "forgotten_channel_takes_new_name"},
	{"part_reason_only_to_parter", "msg := \":\" + c.prefix() + \" PART \" + h.display[key]\n\t\tif len(p) > 1 {\n\t\t\tmsg += \" :\" + p[1]\n\t\t}\n\t\tfor m := range h.channels[key] {\n\t\t\th.send(m, msg)\n\t\t}", "msg := \":\" + c.prefix() + \" PART \" + h.display[key]\n\t\tfor m := range h.channels[key] {\n\t\t\tif m == c && len(p) > 1 {\n\t\t\t\th.send(m, msg+\" :\"+p[1])\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\th.send(m, msg)\n\t\t}", "part_reason_reaches_members"},
	{"quit_keeps_channel_membership", "for ch := range c.channels {\n\t\tdelete(h.channels[ch], c)", "for ch := range c.channels {\n\t\tif reason == \"Connection closed\" {\n\t\t\tdelete(h.channels[ch], c)\n\t\t}", "quit_removes_from_channels"},
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
			mutated := string(src)
			for _, r := range append([][2]string{{m.from, m.to}}, mutantAlso[m.name]...) {
				if !strings.Contains(mutated, r[0]) {
					t.Fatalf("mutation anchor not found in the reference: %q", r[0])
				}
				mutated = strings.Replace(mutated, r[0], r[1], 1)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module mutant\n\ngo 1.22\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mutated), 0o644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "server")
			cmd := exec.Command("go", "build", "-o", bin, ".")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("mutant does not build: %v\n%s", err, out)
			}
			var target *check
			all := append(append([]check{}, checks...), v2Checks...)
			for i := range all {
				if all[i].name == m.mustFail {
					target = &all[i]
				}
			}
			if target == nil {
				t.Fatalf("no check named %q", m.mustFail)
			}
			addr, stop, err := start(bin, "-v2")
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
