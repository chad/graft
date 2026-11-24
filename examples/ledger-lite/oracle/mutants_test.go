package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mutant is a deliberate bug planted in a copy of the reference. The oracle
// must fail the named check on it, so "the oracle has teeth" is a claim
// anyone can re-run. Most are cross-section bugs: a rule stated in one
// section (journal, holds, freezing) dropped by the code of another.
type mutant struct {
	name, from, to string
	mustFail       string
}

var mutants = []mutant{
	{"withdraw_ignores_holds", "\tif a.Balance-a.Held < n {", "\tif a.Balance < n {", "withdraw_respects_available"},
	{"capture_not_journaled", "\t\tl.post(a, -h.Amount, \"capture\")", "\t\ta.Balance -= h.Amount", "capture_takes_money"},
	{"transfer_journal_receiver_first", "\tl.post(a, -n, \"transfer\")\n\tl.post(b, n, \"transfer\")", "\tl.post(b, n, \"transfer\")\n\tl.post(a, -n, \"transfer\")", "journal_records_each_kind"},
	{"refused_transfer_takes_id", "\tif err := l.debitable(a, n); err != nil {\n\t\treturn 0, nil, err\n\t}\n\tl.nextT++", "\tl.nextT++\n\tif err := l.debitable(a, n); err != nil {\n\t\treturn 0, nil, err\n\t}", "transfer_ids_skip_refusals"},
	{"refused_hold_takes_id", "\tif err := l.debitable(a, n); err != nil {\n\t\treturn nil, err\n\t}\n\tl.nextH++", "\tl.nextH++\n\tif err := l.debitable(a, n); err != nil {\n\t\treturn nil, err\n\t}", "hold_ids_skip_refusals"},
	{"frozen_holds_allowed", "\tif err := l.debitable(a, n); err != nil {\n\t\treturn nil, err\n\t}\n\tl.nextH++", "\tif a.Balance-a.Held < n {\n\t\treturn nil, errFunds\n\t}\n\tl.nextH++", "frozen_refuses_transfer_out_and_holds"},
	{"frozen_blocks_deposits", "\tif op == \"deposit\" {\n\t\tl.post(a, n, \"deposit\")", "\tif op == \"deposit\" {\n\t\tif a.Frozen {\n\t\t\treturn nil, errFrozen\n\t\t}\n\t\tl.post(a, n, \"deposit\")", "frozen_still_receives"},
	{"frozen_blocks_capture", "\tdelete(l.holds, id)\n\ta := l.accounts[h.Account]", "\tif l.accounts[h.Account].Frozen {\n\t\treturn nil, errFrozen\n\t}\n\tdelete(l.holds, id)\n\ta := l.accounts[h.Account]", "frozen_settles_existing_holds"},
	{"key_replay_moves_money", "\t\t\tif t.From == from && t.To == to && t.Amount == n {\n\t\t\t\treturn 200, t, nil", "\t\t\tif t.From == from && t.To == to && t.Amount == n {\n\t\t\t\tl.post(l.accounts[from], -n, \"transfer\")\n\t\t\t\tl.post(l.accounts[to], n, \"transfer\")\n\t\t\t\treturn 200, t, nil", "idempotent_key_replay"},
	{"key_reuse_answers_old_transfer", "\t\t\treturn 0, nil, apiErr{409, \"key reused\"}", "\t\t\treturn 200, t, nil", "key_reused_409"},
	{"release_journaled", "\tif op == \"capture\" {\n\t\tl.post(a, -h.Amount, \"capture\")\n\t}", "\tif op == \"capture\" {\n\t\tl.post(a, -h.Amount, \"capture\")\n\t} else {\n\t\tl.post(a, 0, \"capture\")\n\t}", "release_returns_reservation"},
	{"account_journal_unfiltered", "\t\t\t\tif e.Account == p[1] {", "\t\t\t\tif true {", "account_journal_is_filtered"},
	{"available_ignores_held", "\tv.Avail = a.Balance - a.Held", "\tv.Avail = a.Balance", "available_is_balance_minus_held"},
	{"seq_from_zero", "Seq: len(l.journal) + 1", "Seq: len(l.journal)", "journal_records_each_kind"},
	{"empty_journal_null", "\"entries\": nonNil(l.journal)", "\"entries\": l.journal", "empty_journal_is_a_list"},
	{"uppercase_ids", "`^[a-z0-9_-]{1,32}$`", "`^[A-Za-z0-9_-]{1,32}$`", "account_id_validation"},
	{"fractional_amounts", "!ok || f != float64(int64(f)) || f < 1", "!ok || f < 1", "amount_validation"},
	{"no_same_account_check", "\tif from == to {", "\tif false {", "same_account_400"},
	{"duplicate_overwrites", "\tif _, ok := l.accounts[id]; ok {", "\tif false {", "duplicate_account_409"},
	{"hold_settles_twice", "\tdelete(l.holds, id)\n", "", "hold_settles_once"},
	{"refused_withdraw_journaled", "\tif err := l.debitable(a, n); err != nil {\n\t\treturn nil, err\n\t}\n\tl.post(a, -n, \"withdraw\")", "\tif err := l.debitable(a, n); err != nil {\n\t\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Kind: \"withdraw\"})\n\t\treturn nil, err\n\t}\n\tl.post(a, -n, \"withdraw\")", "journal_skips_non_changes"},
	// Escaped the first version of the oracle (review on #29): refusals that
	// leave a trace. wantErr now checks every refusal changed nothing, and
	// nextIDs probes the id counters.
	{"frozen_refusal_journaled", "\tif a.Frozen {\n\t\treturn errFrozen", "\tif a.Frozen {\n\t\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Kind: \"withdraw\"})\n\t\treturn errFrozen", "frozen_refuses_withdraw"},
	{"invalid_amount_journaled", "\tn, err := amount(m)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tif op == \"deposit\" {", "\tn, err := amount(m)\n\tif err != nil {\n\t\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Kind: op})\n\t\treturn nil, err\n\t}\n\tif op == \"deposit\" {", "amount_validation"},
	{"invalid_amount_transfer_takes_id", "\tn, err := amount(m)\n\tif err != nil {\n\t\treturn 0, nil, err\n\t}", "\tn, err := amount(m)\n\tif err != nil {\n\t\tl.nextT++\n\t\treturn 0, nil, err\n\t}", "amount_validation"},
	{"key_reused_takes_id", "\t\t\treturn 0, nil, apiErr{409, \"key reused\"}", "\t\t\tl.nextT++\n\t\t\treturn 0, nil, apiErr{409, \"key reused\"}", "key_reused_409"},
}

func TestMutantsAreCaught(t *testing.T) {
	runMutants(t, "../reference/main.go", checkSet(false), mutants)
}

// runMutants plants each mutant in a copy of the reference at ref and checks
// that the named check, from set, fails on it.
func runMutants(t *testing.T, ref string, set []check, ms []mutant) {
	if testing.Short() {
		t.Skip("short")
	}
	src, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		t.Run(m.name, func(t *testing.T) {
			if strings.Count(string(src), m.from) != 1 {
				t.Fatalf("mutation anchor must occur exactly once in the reference: %q", m.from)
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
			for i := range set {
				if set[i].name == m.mustFail {
					target = &set[i]
				}
			}
			if target == nil {
				t.Fatalf("no check named %q", m.mustFail)
			}
			addr, stop, err := start([]string{bin}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := runCheck(*target, addr); err == nil {
				t.Fatalf("check %s passed on mutant %s: the oracle missed it", m.mustFail, m.name)
			}
		})
	}
}
