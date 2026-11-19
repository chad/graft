// Command oracle is ledger-lite's held-out behaviour suite: it is never shown
// to a model. It starts the server under test once per check (fresh state),
// runs the check over HTTP and prints {"checks","passed","failed","errors"}.
//
//	go run ./oracle -bin S              # a built server binary
//	go run ./oracle -cmd '["python3","main.py"]' -dir D
//	go run ./oracle -addr host:port     # an already-running server (state leaks between checks)
//
// Each check is tagged visible when a check in the visible smoke suite
// exercises the same behaviour; the overlap is printed, so an oracle score
// is never read as fully held out when it is not.
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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// failure aborts a check; run recovers it into the check's error.
type failure struct{ msg string }

func failf(format string, a ...any) { panic(failure{fmt.Sprintf(format, a...)}) }

// srv is a client for one server under test.
type srv struct{ base string }

var client = &http.Client{Timeout: 10 * time.Second}

// do sends a request; body "" sends none. It returns status, decoded JSON
// (nil if the body is not a JSON object) and the Content-Type.
func (s srv) do(method, path, body string) (int, map[string]any, string) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, s.base+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		failf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		m = nil
	}
	return resp.StatusCode, m, resp.Header.Get("Content-Type")
}

// want asserts a status (and, for errors, the error message).
func (s srv) want(status int, method, path, body string) map[string]any {
	got, m, _ := s.do(method, path, body)
	if got != status {
		failf("%s %s %s: status %d, want %d (body %v)", method, path, body, got, status, m)
	}
	return m
}

func (s srv) wantErr(status int, msg, method, path, body string) {
	m := s.want(status, method, path, body)
	if m == nil || m["error"] != msg {
		failf("%s %s %s: body %v, want error %q", method, path, body, m, msg)
	}
}

func num(m map[string]any, k string) int64 {
	n, ok := m[k].(json.Number)
	if !ok {
		failf("field %q missing or not a number in %v", k, m)
	}
	i, err := n.Int64()
	if err != nil {
		failf("field %q not an integer in %v", k, m)
	}
	return i
}

func (s srv) open(id string)                   { s.want(201, "POST", "/accounts", `{"id":"`+id+`"}`) }
func (s srv) deposit(id string, n int)         { s.want(200, "POST", "/accounts/"+id+"/deposit", amt(n)) }
func (s srv) account(id string) map[string]any { return s.want(200, "GET", "/accounts/"+id, "") }
func amt(n int) string                         { return fmt.Sprintf(`{"amount":%d}`, n) }

// expect checks an account's balance, held and available.
func (s srv) expect(id string, balance, held int64) {
	a := s.account(id)
	if num(a, "balance") != balance || num(a, "held") != held || num(a, "available") != balance-held {
		failf("account %s: %v, want balance %d held %d available %d", id, a, balance, held, balance-held)
	}
}

type entry struct {
	Seq     int64
	Account string
	Delta   int64
	Kind    string
}

func (s srv) journal(path string) []entry {
	m := s.want(200, "GET", path, "")
	raw, ok := m["entries"].([]any)
	if !ok {
		failf("GET %s: entries missing or not a list: %v", path, m)
	}
	var out []entry
	for _, r := range raw {
		e, _ := r.(map[string]any)
		if e == nil {
			failf("GET %s: entry is not an object: %v", path, r)
		}
		acct, _ := e["account"].(string)
		kind, _ := e["kind"].(string)
		out = append(out, entry{num(e, "seq"), acct, num(e, "delta"), kind})
	}
	return out
}

func (s srv) journalLen() int { return len(s.journal("/journal")) }

// snapshot is everything a refused request must leave unchanged.
func (s srv) snapshot(ids ...string) string {
	var b strings.Builder
	for _, id := range ids {
		a := s.account(id)
		fmt.Fprintf(&b, "%s:%d/%d/%v ", id, num(a, "balance"), num(a, "held"), a["frozen"])
	}
	fmt.Fprintf(&b, "journal:%d", s.journalLen())
	return b.String()
}

// sumsMatch checks every account's balance equals the sum of its deltas.
func (s srv) sumsMatch(ids ...string) {
	sum := map[string]int64{}
	for _, e := range s.journal("/journal") {
		sum[e.Account] += e.Delta
	}
	for _, id := range ids {
		if b := num(s.account(id), "balance"); b != sum[id] {
			failf("account %s: balance %d but its journal deltas sum to %d", id, b, sum[id])
		}
	}
}

type check struct {
	name    string
	visible bool // a smoke check exercises the same behaviour
	run     func(s srv)
}

var checks = []check{
	// --- Running ---
	{"content_type_json_everywhere", false, func(s srv) {
		for _, r := range [][3]string{{"POST", "/accounts", `{"id":"a"}`}, {"GET", "/accounts/a", ""}, {"GET", "/accounts/zz", ""}, {"GET", "/nope", ""}, {"GET", "/journal", ""}} {
			if _, _, ct := s.do(r[0], r[1], r[2]); !strings.HasPrefix(ct, "application/json") {
				failf("%s %s: Content-Type %q", r[0], r[1], ct)
			}
		}
	}},
	{"invalid_json_400", false, func(s srv) {
		s.wantErr(400, "invalid json", "POST", "/accounts", `nope`)
		s.open("a")
		s.wantErr(400, "invalid json", "POST", "/accounts/a/deposit", `{"amount":`)
		s.wantErr(400, "invalid json", "POST", "/transfers", `[1,2`)
	}},
	{"unknown_path_404", true, func(s srv) {
		s.wantErr(404, "not found", "GET", "/nope", "")
		s.wantErr(404, "not found", "GET", "/accounts/a/b/c", "")
	}},
	{"undefined_method_404", false, func(s srv) {
		s.open("a")
		s.wantErr(404, "not found", "GET", "/transfers", "")
		s.wantErr(404, "not found", "DELETE", "/accounts/a", "")
		s.wantErr(404, "not found", "GET", "/accounts/a/deposit", "")
		s.wantErr(404, "not found", "PUT", "/journal", "")
	}},
	{"concurrent_deposits_no_lost_updates", false, func(s srv) {
		s.open("a")
		parallel(50, func(int) { s.deposit("a", 1) })
		s.expect("a", 50, 0)
		j := s.journal("/journal")
		if len(j) != 50 {
			failf("journal has %d entries after 50 deposits", len(j))
		}
		for i, e := range j {
			if e.Seq != int64(i+1) {
				failf("journal seq %d at position %d: seq must count from 1 with no gaps", e.Seq, i)
			}
		}
	}},
	{"concurrent_transfers_conserve_money", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 1000)
		s.deposit("b", 1000)
		parallel(40, func(i int) {
			from, to := "a", "b"
			if i%2 == 1 {
				from, to = "b", "a"
			}
			s.want(201, "POST", "/transfers", fmt.Sprintf(`{"from":"%s","to":"%s","amount":7}`, from, to))
		})
		s.expect("a", 1000, 0)
		s.expect("b", 1000, 0)
		s.sumsMatch("a", "b")
		for i := 1; i <= 40; i++ {
			s.want(200, "GET", fmt.Sprintf("/transfers/t%d", i), "")
		}
		s.wantErr(404, "no such transfer", "GET", "/transfers/t41", "")
	}},

	// --- Accounts ---
	{"create_account_view", true, func(s srv) {
		m := s.want(201, "POST", "/accounts", `{"id":"alice"}`)
		if m["id"] != "alice" || num(m, "balance") != 0 || num(m, "held") != 0 || num(m, "available") != 0 || m["frozen"] != false {
			failf("new account view %v", m)
		}
		if a := s.account("alice"); a["id"] != "alice" || a["frozen"] != false {
			failf("GET view %v", a)
		}
	}},
	{"account_id_validation", false, func(s srv) {
		for _, b := range []string{`{"id":""}`, `{"id":"` + strings.Repeat("a", 33) + `"}`, `{"id":"Bob"}`, `{"id":"a b"}`, `{"id":"a.b"}`, `{}`, `{"id":5}`} {
			s.wantErr(400, "invalid id", "POST", "/accounts", b)
		}
		s.want(201, "POST", "/accounts", `{"id":"`+strings.Repeat("z", 32)+`"}`)
		s.want(201, "POST", "/accounts", `{"id":"a_b-9"}`)
	}},
	{"duplicate_account_409", false, func(s srv) {
		s.open("a")
		s.deposit("a", 10)
		s.wantErr(409, "account exists", "POST", "/accounts", `{"id":"a"}`)
		s.expect("a", 10, 0)
	}},
	{"unknown_account_404_everywhere", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		for _, r := range [][3]string{
			{"GET", "/accounts/ghost", ""}, {"POST", "/accounts/ghost/deposit", amt(5)}, {"POST", "/accounts/ghost/withdraw", amt(5)},
			{"POST", "/accounts/ghost/holds", amt(5)}, {"GET", "/accounts/ghost/journal", ""}, {"POST", "/accounts/ghost/freeze", ""},
			{"POST", "/accounts/ghost/unfreeze", ""}, {"POST", "/transfers", `{"from":"ghost","to":"a","amount":5}`},
			{"POST", "/transfers", `{"from":"a","to":"ghost","amount":5}`},
		} {
			s.wantErr(404, "no such account", r[0], r[1], r[2])
		}
		s.expect("a", 100, 0)
		if n := s.journalLen(); n != 1 {
			failf("journal has %d entries; refused requests must not append", n)
		}
	}},
	{"available_is_balance_minus_held", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(30))
		s.expect("a", 100, 30)
	}},

	// --- Deposits and withdrawals ---
	{"deposit_adds", true, func(s srv) {
		s.open("a")
		m := s.want(200, "POST", "/accounts/a/deposit", amt(250))
		if num(m, "balance") != 250 || num(m, "available") != 250 {
			failf("deposit answered %v", m)
		}
		s.deposit("a", 50)
		s.expect("a", 300, 0)
	}},
	{"withdraw_subtracts", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		m := s.want(200, "POST", "/accounts/a/withdraw", amt(40))
		if num(m, "balance") != 60 {
			failf("withdraw answered %v", m)
		}
		s.expect("a", 60, 0)
	}},
	{"amount_validation", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 500)
		for _, b := range []string{`{"amount":0}`, `{"amount":-1}`, `{"amount":100000001}`, `{"amount":1.5}`, `{"amount":"10"}`, `{}`, `{"amount":null}`} {
			s.wantErr(400, "invalid amount", "POST", "/accounts/a/deposit", b)
			s.wantErr(400, "invalid amount", "POST", "/accounts/a/withdraw", b)
			s.wantErr(400, "invalid amount", "POST", "/accounts/a/holds", b)
		}
		s.wantErr(400, "invalid amount", "POST", "/transfers", `{"from":"a","to":"b","amount":0}`)
		s.expect("a", 500, 0)
		s.want(200, "POST", "/accounts/a/deposit", amt(100000000))
		s.expect("a", 100000500, 0)
	}},
	{"withdraw_respects_available", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(60))
		s.wantErr(409, "insufficient funds", "POST", "/accounts/a/withdraw", amt(50))
		s.expect("a", 100, 60)
		s.want(200, "POST", "/accounts/a/withdraw", amt(40))
		s.expect("a", 60, 60)
	}},
	{"withdraw_beyond_balance_409", true, func(s srv) {
		s.open("a")
		s.deposit("a", 10)
		s.wantErr(409, "insufficient funds", "POST", "/accounts/a/withdraw", amt(11))
		s.expect("a", 10, 0)
	}},

	// --- Transfers ---
	{"transfer_moves_money", true, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		m := s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":30}`)
		if m["id"] != "t1" || m["from"] != "a" || m["to"] != "b" || num(m, "amount") != 30 {
			failf("transfer answered %v", m)
		}
		s.expect("a", 70, 0)
		s.expect("b", 30, 0)
	}},
	{"transfer_ids_skip_refusals", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":1}`)
		s.wantErr(409, "insufficient funds", "POST", "/transfers", `{"from":"a","to":"b","amount":1000}`)
		s.wantErr(400, "same account", "POST", "/transfers", `{"from":"a","to":"a","amount":1}`)
		s.wantErr(404, "no such account", "POST", "/transfers", `{"from":"a","to":"zz","amount":1}`)
		if m := s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":1}`); m["id"] != "t2" {
			failf("second successful transfer has id %v; refused transfers must not take an id", m["id"])
		}
	}},
	{"same_account_400", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.wantErr(400, "same account", "POST", "/transfers", `{"from":"a","to":"a","amount":5}`)
		s.expect("a", 100, 0)
	}},
	{"transfer_insufficient_changes_nothing", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 50)
		before := s.snapshot("a", "b")
		s.wantErr(409, "insufficient funds", "POST", "/transfers", `{"from":"a","to":"b","amount":51}`)
		if after := s.snapshot("a", "b"); after != before {
			failf("refused transfer changed state: %s → %s", before, after)
		}
	}},
	{"transfer_respects_holds", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(80))
		s.wantErr(409, "insufficient funds", "POST", "/transfers", `{"from":"a","to":"b","amount":21}`)
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":20}`)
		s.expect("a", 80, 80)
	}},
	{"idempotent_key_replay", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		first := s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":10,"key":"k1"}`)
		again := s.want(200, "POST", "/transfers", `{"from":"a","to":"b","amount":10,"key":"k1"}`)
		if again["id"] != first["id"] || num(again, "amount") != 10 {
			failf("replay answered %v, want the first transfer %v", again, first)
		}
		s.expect("a", 90, 0)
		s.expect("b", 10, 0)
		if n := s.journalLen(); n != 3 {
			failf("journal has %d entries; a replay must not append", n)
		}
		if m := s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":1}`); m["id"] != "t2" {
			failf("next transfer has id %v; a replay must not take an id", m["id"])
		}
	}},
	{"key_reused_409", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.open("c")
		s.deposit("a", 100)
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":10,"key":"k1"}`)
		before := s.snapshot("a", "b", "c")
		s.wantErr(409, "key reused", "POST", "/transfers", `{"from":"a","to":"b","amount":11,"key":"k1"}`)
		s.wantErr(409, "key reused", "POST", "/transfers", `{"from":"a","to":"c","amount":10,"key":"k1"}`)
		if after := s.snapshot("a", "b", "c"); after != before {
			failf("key reuse changed state: %s → %s", before, after)
		}
	}},
	{"get_transfer", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":25}`)
		m := s.want(200, "GET", "/transfers/t1", "")
		if m["id"] != "t1" || m["from"] != "a" || m["to"] != "b" || num(m, "amount") != 25 {
			failf("GET /transfers/t1: %v", m)
		}
		s.wantErr(404, "no such transfer", "GET", "/transfers/t2", "")
		s.wantErr(404, "no such transfer", "GET", "/transfers/nope", "")
	}},

	// --- Holds ---
	{"hold_reserves", true, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		m := s.want(201, "POST", "/accounts/a/holds", amt(40))
		if m["id"] != "h1" || m["account"] != "a" || num(m, "amount") != 40 {
			failf("hold answered %v", m)
		}
		s.expect("a", 100, 40)
	}},
	{"hold_ids_skip_refusals", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(10))
		s.wantErr(409, "insufficient funds", "POST", "/accounts/a/holds", amt(1000))
		if m := s.want(201, "POST", "/accounts/a/holds", amt(10)); m["id"] != "h2" {
			failf("second hold has id %v", m["id"])
		}
	}},
	{"hold_insufficient_409", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(70))
		s.wantErr(409, "insufficient funds", "POST", "/accounts/a/holds", amt(31))
		s.expect("a", 100, 70)
	}},
	{"capture_takes_money", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(30))
		m := s.want(200, "POST", "/holds/h1/capture", "")
		if num(m, "balance") != 70 || num(m, "held") != 0 {
			failf("capture answered %v", m)
		}
		s.expect("a", 70, 0)
		j := s.journal("/journal")
		if last := j[len(j)-1]; last.Kind != "capture" || last.Delta != -30 || last.Account != "a" {
			failf("capture journal entry %+v", last)
		}
	}},
	{"release_returns_reservation", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(30))
		m := s.want(200, "POST", "/holds/h1/release", "")
		if num(m, "balance") != 100 || num(m, "held") != 0 {
			failf("release answered %v", m)
		}
		s.expect("a", 100, 0)
		if n := s.journalLen(); n != 1 {
			failf("journal has %d entries; a release must not append", n)
		}
	}},
	{"hold_settles_once", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(10))
		s.want(201, "POST", "/accounts/a/holds", amt(20))
		s.want(200, "POST", "/holds/h1/capture", "")
		s.wantErr(404, "no such hold", "POST", "/holds/h1/capture", "")
		s.wantErr(404, "no such hold", "POST", "/holds/h1/release", "")
		s.want(200, "POST", "/holds/h2/release", "")
		s.wantErr(404, "no such hold", "POST", "/holds/h2/capture", "")
		s.wantErr(404, "no such hold", "POST", "/holds/h9/release", "")
		s.expect("a", 90, 0)
	}},
	{"holds_are_independent", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.deposit("b", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(10))
		s.want(201, "POST", "/accounts/b/holds", amt(20))
		s.want(201, "POST", "/accounts/a/holds", amt(30))
		s.want(200, "POST", "/holds/h3/capture", "")
		s.expect("a", 70, 10)
		s.expect("b", 100, 20)
	}},

	// --- Journal ---
	{"journal_records_each_kind", true, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/a/withdraw", amt(10))
		s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":20}`)
		s.want(201, "POST", "/accounts/b/holds", amt(5))
		s.want(200, "POST", "/holds/h1/capture", "")
		want := []entry{{1, "a", 100, "deposit"}, {2, "a", -10, "withdraw"}, {3, "a", -20, "transfer"}, {4, "b", 20, "transfer"}, {5, "b", -5, "capture"}}
		if got := s.journal("/journal"); fmt.Sprint(got) != fmt.Sprint(want) {
			failf("journal %+v, want %+v", got, want)
		}
	}},
	{"journal_skips_non_changes", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(10))
		s.want(200, "POST", "/holds/h1/release", "")
		s.wantErr(409, "insufficient funds", "POST", "/accounts/a/withdraw", amt(1000))
		s.wantErr(409, "insufficient funds", "POST", "/transfers", `{"from":"a","to":"b","amount":1000}`)
		s.wantErr(409, "account exists", "POST", "/accounts", `{"id":"b"}`)
		s.want(200, "POST", "/accounts/b/freeze", "")
		s.want(200, "POST", "/accounts/b/unfreeze", "")
		if n := s.journalLen(); n != 1 {
			failf("journal has %d entries; only the deposit changed a balance", n)
		}
	}},
	{"balance_equals_journal_sum", false, func(s srv) {
		for _, id := range []string{"a", "b", "c"} {
			s.open(id)
			s.deposit(id, 500)
		}
		ops := [][3]string{
			{"POST", "/transfers", `{"from":"a","to":"b","amount":120}`}, {"POST", "/accounts/c/withdraw", amt(70)},
			{"POST", "/accounts/b/holds", amt(200)}, {"POST", "/transfers", `{"from":"b","to":"c","amount":300}`},
			{"POST", "/holds/h1/capture", ""}, {"POST", "/transfers", `{"from":"c","to":"a","amount":1}`},
			{"POST", "/accounts/a/holds", amt(50)}, {"POST", "/holds/h2/release", ""},
		}
		for _, o := range ops {
			s.do(o[0], o[1], o[2])
		}
		s.sumsMatch("a", "b", "c")
	}},
	{"account_journal_is_filtered", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.deposit("b", 50)
		s.want(201, "POST", "/transfers", `{"from":"b","to":"a","amount":5}`)
		want := []entry{{2, "b", 50, "deposit"}, {3, "b", -5, "transfer"}}
		if got := s.journal("/accounts/b/journal"); fmt.Sprint(got) != fmt.Sprint(want) {
			failf("b's journal %+v, want %+v (global seq numbers, only b's entries)", got, want)
		}
	}},
	{"empty_journal_is_a_list", false, func(s srv) {
		if got := s.journal("/journal"); len(got) != 0 {
			failf("fresh journal %v", got)
		}
		s.open("a")
		if got := s.journal("/accounts/a/journal"); len(got) != 0 {
			failf("new account's journal %v", got)
		}
	}},

	// --- Freezing ---
	{"freeze_and_unfreeze_view", true, func(s srv) {
		s.open("a")
		if m := s.want(200, "POST", "/accounts/a/freeze", ""); m["frozen"] != true {
			failf("freeze answered %v", m)
		}
		if a := s.account("a"); a["frozen"] != true {
			failf("frozen account view %v", a)
		}
		if m := s.want(200, "POST", "/accounts/a/unfreeze", ""); m["frozen"] != false {
			failf("unfreeze answered %v", m)
		}
	}},
	{"frozen_refuses_withdraw", true, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/a/freeze", "")
		s.wantErr(423, "account frozen", "POST", "/accounts/a/withdraw", amt(10))
		s.expect("a", 100, 0)
	}},
	{"frozen_refuses_transfer_out_and_holds", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/a/freeze", "")
		before := s.snapshot("a", "b")
		s.wantErr(423, "account frozen", "POST", "/transfers", `{"from":"a","to":"b","amount":10}`)
		s.wantErr(423, "account frozen", "POST", "/accounts/a/holds", amt(10))
		if after := s.snapshot("a", "b"); after != before {
			failf("refused operations changed state: %s → %s", before, after)
		}
		s.want(200, "POST", "/accounts/a/unfreeze", "")
		if m := s.want(201, "POST", "/transfers", `{"from":"a","to":"b","amount":10}`); m["id"] != "t1" {
			failf("first successful transfer has id %v; refused ones must not take ids", m["id"])
		}
		if m := s.want(201, "POST", "/accounts/a/holds", amt(10)); m["id"] != "h1" {
			failf("first successful hold has id %v; refused ones must not take ids", m["id"])
		}
	}},
	{"frozen_still_receives", false, func(s srv) {
		s.open("a")
		s.open("b")
		s.deposit("b", 100)
		s.want(200, "POST", "/accounts/a/freeze", "")
		s.want(200, "POST", "/accounts/a/deposit", amt(5))
		s.want(201, "POST", "/transfers", `{"from":"b","to":"a","amount":20}`)
		s.expect("a", 25, 0)
		s.sumsMatch("a", "b")
	}},
	{"frozen_settles_existing_holds", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(201, "POST", "/accounts/a/holds", amt(10))
		s.want(201, "POST", "/accounts/a/holds", amt(20))
		s.want(200, "POST", "/accounts/a/freeze", "")
		s.want(200, "POST", "/holds/h1/capture", "")
		s.want(200, "POST", "/holds/h2/release", "")
		s.expect("a", 90, 0)
	}},
	{"unfreeze_restores_debits", false, func(s srv) {
		s.open("a")
		s.deposit("a", 100)
		s.want(200, "POST", "/accounts/a/freeze", "")
		s.want(200, "POST", "/accounts/a/unfreeze", "")
		s.want(200, "POST", "/accounts/a/withdraw", amt(10))
		s.expect("a", 90, 0)
	}},
}

// parallel runs f(0..n-1) concurrently and re-raises the first failure.
func parallel(n int, f func(i int)) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first any
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if first == nil {
						first = r
					}
					mu.Unlock()
				}
			}()
			f(i)
		}(i)
	}
	wg.Wait()
	if first != nil {
		panic(first)
	}
}

// runCheck runs one check against addr and returns its failure, if any.
func runCheck(c check, addr string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if f, ok := r.(failure); ok {
				err = fmt.Errorf("%s", f.msg)
				return
			}
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	c.run(srv{base: "http://" + addr})
	return nil
}

// serverArgv substitutes {addr}, or appends "-addr addr" when there is none.
func serverArgv(argv []string, addr string) []string {
	out, placed := make([]string, len(argv)), false
	for i, a := range argv {
		out[i] = strings.ReplaceAll(a, "{addr}", addr)
		placed = placed || out[i] != a
	}
	if !placed {
		out = append(out, "-addr", addr)
	}
	return out
}

// start launches the server on a free port and waits until it accepts.
func start(argv []string, dir string) (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	addr := l.Addr().String()
	l.Close()
	full := serverArgv(argv, addr)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	stop := func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Process.Kill(); cmd.Wait() }
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return addr, stop, nil
		}
	}
	stop()
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > 1500 {
		msg = "..." + msg[len(msg)-1500:]
	}
	return "", nil, fmt.Errorf("server did not listen on %s; its stderr:\n%s", addr, msg)
}

type result struct {
	Checks  int               `json:"checks"`
	Passed  int               `json:"passed"`
	Failed  []string          `json:"failed"`
	Visible int               `json:"visible"`        // checks whose behaviour a smoke check also exercises
	VisPass int               `json:"visible_passed"` // of those, passed
	Errors  map[string]string `json:"errors,omitempty"`
}

func main() {
	bin := flag.String("bin", "", "server binary to start once per check")
	cmdJSON := flag.String("cmd", "", `server start command as a JSON argv; "-addr A" is appended unless an argument holds {addr}`)
	dir := flag.String("dir", "", "working directory for -cmd")
	addr := flag.String("addr", "", "address of an already-running server")
	only := flag.String("run", "", "run only checks whose name contains this")
	list := flag.Bool("list", false, "list the checks (name, visible overlap) and exit")
	flag.Parse()
	if *list {
		for _, c := range checks {
			fmt.Printf("%-40s visible=%v\n", c.name, c.visible)
		}
		return
	}
	var argv []string
	switch {
	case *cmdJSON != "":
		if err := json.Unmarshal([]byte(*cmdJSON), &argv); err != nil || len(argv) == 0 {
			fmt.Fprintln(os.Stderr, "oracle: -cmd must be a non-empty JSON array of strings")
			os.Exit(2)
		}
	case *bin != "":
		argv = []string{*bin}
	}
	if (argv == nil) == (*addr == "") {
		fmt.Fprintln(os.Stderr, "oracle: exactly one of -bin, -cmd or -addr")
		os.Exit(2)
	}
	r := result{Failed: []string{}, Errors: map[string]string{}}
	for _, c := range checks {
		if *only != "" && !strings.Contains(c.name, *only) {
			continue
		}
		a, stop := *addr, func() {}
		if argv != nil {
			var err error
			if a, stop, err = start(argv, *dir); err != nil {
				fmt.Fprintln(os.Stderr, "oracle:", err)
				os.Exit(2)
			}
		}
		r.Checks++
		if c.visible {
			r.Visible++
		}
		if err := runCheck(c, a); err != nil {
			r.Failed = append(r.Failed, c.name)
			r.Errors[c.name] = err.Error()
		} else {
			r.Passed++
			if c.visible {
				r.VisPass++
			}
		}
		stop()
	}
	sort.Strings(r.Failed)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(r)
	if r.Passed < r.Checks {
		os.Exit(1)
	}
}
