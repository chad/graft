package main

import "testing"

// v2Mutants are planted in reference-v2 and must each fail a named -v2 check.
// They are the v2 revision's cross-section drops: a rule stated in Freezing or
// Journal that the code of another section (deposits, transfers, holds, the
// account journal route) forgets.
var v2Mutants = []mutant{
	{"deposit_ignores_frozen", "\t\tif err := l.creditable(a); err != nil {\n\t\t\treturn nil, err\n\t\t}\n", "", "frozen_refuses_incoming"},
	{"transfer_in_ignores_frozen", "\tif err := l.creditable(b); err != nil {\n\t\treturn 0, nil, err\n\t}\n", "", "frozen_refuses_incoming"},
	{"transfer_in_checked_after_debit",
		"\tif err := l.creditable(b); err != nil {\n\t\treturn 0, nil, err\n\t}\n\tl.nextT++\n\tt := &transfer{ID: \"t\" + strconv.Itoa(l.nextT), From: from, To: to, Amount: n, key: key}\n\tl.transfers[t.ID] = t\n\tif key != \"\" {\n\t\tl.byKey[key] = t\n\t}\n\tl.post(a, -n, \"transfer\")\n",
		"\tl.nextT++\n\tt := &transfer{ID: \"t\" + strconv.Itoa(l.nextT), From: from, To: to, Amount: n, key: key}\n\tl.transfers[t.ID] = t\n\tif key != \"\" {\n\t\tl.byKey[key] = t\n\t}\n\tl.post(a, -n, \"transfer\")\n\tif err := l.creditable(b); err != nil {\n\t\treturn 0, nil, err\n\t}\n",
		"frozen_receiver_transfer_leaves_sender_alone"},
	{"frozen_incoming_takes_id", "\tif err := l.creditable(b); err != nil {\n\t\treturn 0, nil, err\n\t}\n\tl.nextT++", "\tif err := l.creditable(b); err != nil {\n\t\tl.nextT++\n\t\treturn 0, nil, err\n\t}\n\tl.nextT++", "frozen_receiver_transfer_leaves_sender_alone"},
	{"frozen_incoming_deposit_journaled", "\t\tif err := l.creditable(a); err != nil {\n\t\t\treturn nil, err\n\t\t}", "\t\tif err := l.creditable(a); err != nil {\n\t\t\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Kind: \"deposit\"})\n\t\t\treturn nil, err\n\t\t}", "frozen_refuses_incoming"},
	{"frozen_blocks_capture", "\tdelete(l.holds, id)\n\ta := l.accounts[h.Account]", "\tif l.accounts[h.Account].Frozen {\n\t\treturn nil, errFrozen\n\t}\n\tdelete(l.holds, id)\n\ta := l.accounts[h.Account]", "frozen_settles_existing_holds"},
	{"frozen_blocks_release", "\ta := l.accounts[h.Account]\n\ta.Held -= h.Amount", "\ta := l.accounts[h.Account]\n\tif op == \"release\" && a.Frozen {\n\t\treturn nil, errFrozen\n\t}\n\ta.Held -= h.Amount", "frozen_settles_existing_holds"},
	{"unfreeze_still_blocks_credits", "func (l *ledger) creditable(a *account) error {\n\tif a.Frozen {", "var everFrozen = map[string]bool{}\n\nfunc (l *ledger) creditable(a *account) error {\n\tif a.Frozen {\n\t\teverFrozen[a.ID] = true\n\t}\n\tif everFrozen[a.ID] {", "unfreeze_restores_credits"},
	{"transfer_entries_lack_balance", "Delta: delta, Balance: a.Balance, Kind: kind})", "Delta: delta, Balance: a.Balance, Kind: kind})\n\tif kind == \"transfer\" {\n\t\tl.journal[len(l.journal)-1].Balance = 0\n\t}", "journal_entries_carry_balance"},
	{"capture_entry_lacks_balance", "\t\tl.post(a, -h.Amount, \"capture\")", "\t\ta.Balance -= h.Amount\n\t\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Delta: -h.Amount, Kind: \"capture\"})", "journal_balance_after_capture"},
	{"balance_is_available", "Delta: delta, Balance: a.Balance, Kind: kind})", "Delta: delta, Balance: a.Balance - a.Held, Kind: kind})", "journal_balance_is_balance_not_available"},
	{"balance_before_change", "\ta.Balance += delta\n\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Delta: delta, Balance: a.Balance, Kind: kind})", "\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Delta: delta, Balance: a.Balance, Kind: kind})\n\ta.Balance += delta", "journal_balance_chain"},
	{"receiver_entry_has_sender_balance", "\tl.post(b, n, \"transfer\")", "\tl.post(b, n, \"transfer\")\n\tl.journal[len(l.journal)-1].Balance = a.Balance", "journal_balance_chain"},
	{"account_journal_drops_balance", "\t\t\tout = map[string]any{\"entries\": nonNil(es)}", "\t\t\tslim := []map[string]any{}\n\t\t\tfor _, e := range es {\n\t\t\t\tslim = append(slim, map[string]any{\"seq\": e.Seq, \"account\": e.Account, \"delta\": e.Delta, \"kind\": e.Kind})\n\t\t\t}\n\t\t\tout = map[string]any{\"entries\": slim}", "account_journal_carries_balance"},
}

func TestV2MutantsAreCaught(t *testing.T) {
	runMutants(t, "../reference-v2/main.go", checkSet(true), v2Mutants)
}

// The v1 mutants still bite under -v2, on the v2 reference: a v2 server that
// regressed a v1 rule is caught. frozen_blocks_deposits is not carried over (it
// is now the reference's behaviour); two whose anchors the new credit check
// breaks are restated against reference-v2.
func TestV1MutantsAreCaughtByV2Oracle(t *testing.T) {
	restated := map[string]mutant{
		"refused_transfer_takes_id": {"refused_transfer_takes_id", "\tif err := l.debitable(a, n); err != nil {\n\t\treturn 0, nil, err\n\t}\n\tif err := l.creditable(b)", "\tl.nextT++\n\tif err := l.debitable(a, n); err != nil {\n\t\treturn 0, nil, err\n\t}\n\tif err := l.creditable(b)", "transfer_ids_skip_refusals"},
		"frozen_refusal_journaled":  {"frozen_refusal_journaled", "func (l *ledger) debitable(a *account, n int64) error {\n\tif a.Frozen {\n\t\treturn errFrozen", "func (l *ledger) debitable(a *account, n int64) error {\n\tif a.Frozen {\n\t\tl.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Kind: \"withdraw\"})\n\t\treturn errFrozen", "frozen_refuses_withdraw"},
	}
	var ms []mutant
	for _, m := range mutants {
		if m.name == "frozen_blocks_deposits" {
			continue
		}
		if r, ok := restated[m.name]; ok {
			m = r
		}
		ms = append(ms, m)
	}
	runMutants(t, "../reference-v2/main.go", checkSet(true), ms)
}
