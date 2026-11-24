package main

import "fmt"

// v2 is the revision of the spec in spec-v2/, run with -v2: a frozen account
// also refuses money coming in (deposits and incoming transfers), and every
// journal entry carries "balance", the account's balance right after the change.
//
// v2Checks replaces the one v1 check that no longer holds, frozen_still_receives,
// and adds the new ones; every other v1 check still holds and stays in the set
// (a regression counts). The entry struct that v1 compares by value is left
// alone: balance is read by journalBal.
//
// None of these is tagged visible: the smoke suite reads a journal entry's
// account, delta and kind only, and touches neither money coming into a frozen
// account nor the balance field. They are all held out.

// v2Dropped are the v1 checks whose behaviour the revision reverses.
var v2Dropped = map[string]bool{"frozen_still_receives": true}

// checkSet is the checks a run scores.
func checkSet(v2 bool) []check {
	if !v2 {
		return checks
	}
	var out []check
	for _, c := range checks {
		if !v2Dropped[c.name] {
			out = append(out, c)
		}
	}
	return append(out, v2Checks...)
}

type entryB struct {
	Seq     int64
	Account string
	Delta   int64
	Balance int64
	Kind    string
}

// journalBal reads a journal route, including each entry's balance.
func (s *srv) journalBal(path string) []entryB {
	m := s.want(200, "GET", path, "")
	raw, ok := m["entries"].([]any)
	if !ok {
		failf("GET %s: entries missing or not a list: %v", path, m)
	}
	var out []entryB
	for _, r := range raw {
		e, _ := r.(map[string]any)
		if e == nil {
			failf("GET %s: entry is not an object: %v", path, r)
		}
		acct, _ := e["account"].(string)
		kind, _ := e["kind"].(string)
		if _, ok := e["balance"]; !ok {
			failf("GET %s: entry %v has no balance", path, e)
		}
		out = append(out, entryB{num(e, "seq"), acct, num(e, "delta"), num(e, "balance"), kind})
	}
	return out
}

var v2Checks = []check{
	{"frozen_refuses_incoming", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/b/freeze", "")
		s.wantErr(423, "account frozen", "POST", "/accounts/b/deposit", amt(5))
		s.wantErr(423, "account frozen", "POST", "/transfers", `{"from":"a","to":"b","amount":20}`)
		s.expect("a", 100, 0)
		s.expect("b", 0, 0)
		s.nextIDs("t1", "h1")
	}},
	{"frozen_receiver_transfer_leaves_sender_alone", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(10))
		s.want(200, "POST", "/accounts/b/freeze", "")
		before := s.snapshot("a", "b")
		s.wantErr(423, "account frozen", "POST", "/transfers", `{"from":"a","to":"b","amount":30,"key":"k1"}`)
		if after := s.snapshot("a", "b"); after != before {
			failf("a transfer refused for its frozen receiver changed state: %s → %s", before, after)
		}
		s.expect("a", 100, 10)
		// The refused transfer took neither an id nor its key.
		s.want(200, "POST", "/accounts/b/unfreeze", "")
		if m := s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":30,"key":"k1"}`); m["id"] != "t1" {
			failf("first successful transfer has id %v; a refused one must not take an id or its key", m["id"])
		}
		s.expect("a", 70, 10)
		s.expect("b", 30, 0)
	}},
	{"frozen_both_directions", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.deposit("b", 100)
		s.want(200, "POST", "/accounts/a/freeze", "")
		s.want(200, "POST", "/accounts/b/freeze", "")
		s.wantErr(423, "account frozen", "POST", "/transfers", `{"from":"a","to":"b","amount":10}`)
		s.wantErr(423, "account frozen", "POST", "/transfers", `{"from":"b","to":"a","amount":10}`)
		s.wantErr(423, "account frozen", "POST", "/accounts/a/deposit", amt(1))
		s.wantErr(423, "account frozen", "POST", "/accounts/a/withdraw", amt(1))
		s.nextIDs("t1", "h1")
	}},
	{"unfreeze_restores_credits", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/b/freeze", "")
		s.wantErr(423, "account frozen", "POST", "/accounts/b/deposit", amt(5))
		s.want(200, "POST", "/accounts/b/unfreeze", "")
		s.deposit("b", 5)
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":20}`)
		s.expect("b", 25, 0)
		s.sumsMatch("a", "b")
	}},
	{"journal_entries_carry_balance", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/a/withdraw", amt(10))
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":20}`)
		s.want(201, "POST", "/accounts/b/holds", amt(5))
		s.want(200, "POST", "/holds/h1/capture", "")
		want := []entryB{{1, "a", 100, 100, "deposit"}, {2, "a", -10, 90, "withdraw"}, {3, "a", -20, 70, "transfer"}, {4, "b", 20, 20, "transfer"}, {5, "b", -5, 15, "capture"}}
		if got := s.journalBal("/journal"); fmt.Sprint(got) != fmt.Sprint(want) {
			failf("journal %+v, want %+v", got, want)
		}
	}},
	{"journal_balance_is_balance_not_available", false, func(s *srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(40))
		s.want(200, "POST", "/accounts/a/withdraw", amt(10))
		got := s.journalBal("/journal")
		if len(got) != 2 || got[1].Balance != 90 {
			failf("after a hold of 40 and a withdrawal of 10 the entry's balance should be 90 (the balance, not the available amount): %+v", got)
		}
	}},
	{"journal_balance_after_capture", false, func(s *srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(40))
		s.want(200, "POST", "/holds/h1/capture", "")
		got := s.journalBal("/journal")
		if len(got) != 2 || got[1].Kind != "capture" || got[1].Balance != 60 {
			failf("the capture entry's balance should be 60, the balance after the capture: %+v", got)
		}
	}},
	{"account_journal_carries_balance", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":30}`)
		got := s.journalBal("/accounts/b/journal")
		want := []entryB{{3, "b", 30, 30, "transfer"}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			failf("account journal %+v, want %+v", got, want)
		}
	}},
	{"journal_balance_zero", false, func(s *srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 50)
		s.want(200, "POST", "/accounts/a/withdraw", amt(20))
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":30}`)
		// every entry must carry "balance", including 0: an entry that leaves the
		// field out when the balance is zero (omitempty) is missing a value.
		got := s.journalBal("/journal")
		if len(got) != 4 || got[3].Account != "b" || got[2].Account != "a" || got[2].Balance != 0 {
			failf("after draining a to exactly 0 the transfer's entry for a must carry balance 0: %+v", got)
		}
	}},
	{"journal_balance_chain", false, func(s *srv) {
		for _, id := range []string{"a", "b"} {
			s.open(id)
		}
		for _, r := range [][4]string{
			{"POST", "/accounts/a/deposit", amt(100), ""}, {"POST", "/accounts/a/withdraw", amt(10), ""},
			{"POST", "/transfers", `{"from":"a","to":"b","amount":20}`, ""}, {"POST", "/accounts/b/holds", amt(5), ""},
			{"POST", "/holds/h1/capture", "", ""}, {"POST", "/accounts/b/deposit", amt(7), ""},
			{"POST", "/transfers", `{"from":"b","to":"a","amount":3}`, ""}, {"POST", "/accounts/a/withdraw", amt(1), ""},
		} {
			s.do(r[0], r[1], r[2])
		}
		prev := map[string]int64{}
		for _, e := range s.journalBal("/journal") {
			if e.Balance != prev[e.Account]+e.Delta {
				failf("entry %+v: balance %d, but the previous balance %d plus delta %d is %d", e, e.Balance, prev[e.Account], e.Delta, prev[e.Account]+e.Delta)
			}
			prev[e.Account] = e.Balance
		}
		for _, id := range []string{"a", "b"} {
			if b := num(s.account(id), "balance"); prev[id] != b {
				failf("account %s: last journal balance %d, but its balance is %d", id, prev[id], b)
			}
		}
	}},
}
