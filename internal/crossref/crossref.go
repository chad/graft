// Package crossref is graft's cross-reference step (SPEC §12): for each new or
// revised statement, which OTHER record's behaviour does it constrain?
//
// It proposes and never applies. A model answers, the answer is validated for
// form (never for truth), and a human accepts it through `graft run --accept
// --crossref`, which copies the statement into the owner's draft. Everything
// the model says is a claim: the quoted statement is re-derived from the spec,
// the target must exist, the location must lie in the target's file, the
// quote must occur in the target's record. A wrong but well-formed answer
// passes; that limit is stated in SPEC §12 and covered by printing every
// "local" verdict.
package crossref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/chad/graft/internal/model"
)

// Verdicts.
const (
	Local      = "local"      // the statement is about its own section's behaviour only
	Constrains = "constrains" // it also binds the behaviour of the target records
)

// Statuses an answer carries after validation.
const (
	OK         = "ok"
	Unanswered = "UNANSWERED" // a changed statement with no answer
	Dangling   = "DANGLING"   // a claim that does not resolve (unknown record, bad location, quote not found, ...)
	Stale      = "STALE"      // an answer for a statement that is not among the changed ones
)

// Stmt is a statement that is new or changed since the last accept.
type Stmt struct {
	Section string // heading path of the section that states it
	Hash    string // clause hash
	Type    string // drafter type (constraint, invariant, ...)
	Text    string
	Own     string // record id of the section's own record; "" for a New section
}

// Rec is one record a statement might constrain, as the model and the
// validator see it.
type Rec struct {
	ID          string // "" for a section that does not have a record yet
	Section     string
	File        string // the one file the record owns; "" while the record is New
	Intent      string
	Constraints []string
	Imports     []string // statements already copied into it from other sections
}

// Target is one record a statement constrains, with the receipt for the claim.
type Target struct {
	Record   string `json:"record"`   // a record id, or a section heading (also for a New section)
	Reason   string `json:"reason"`   // why the statement binds that record's behaviour
	Location string `json:"location"` // "file.go: Ident": the code that must change
	Quote    string `json:"quote"`    // a phrase of the target's record the statement binds
}

// Answer is the proposal for one changed statement.
type Answer struct {
	Section string   `json:"section"`
	Hash    string   `json:"clause_hash"`
	Text    string   `json:"text"` // the statement, quoted verbatim from the spec
	Verdict string   `json:"verdict"`
	Targets []Target `json:"targets,omitempty"`
	Status  string   `json:"status"`           // set by Validate; never trusted when read back
	Detail  string   `json:"detail,omitempty"` // why the status is not ok
}

// Proposal is crossref.json.
type Proposal struct {
	Schema  int      `json:"schema"`
	Answers []Answer `json:"answers"`
}

// SchemaVersion is the current Proposal.Schema.
const SchemaVersion = 1

// Resolve finds the record a target names: by id, else by section heading
// (the full heading path or its last " › " part, case-insensitively), the form
// used for a section that is New in the same change.
func Resolve(recs []Rec, name string) *Rec {
	name = strings.TrimSpace(name)
	for i := range recs {
		if recs[i].ID != "" && recs[i].ID == name {
			return &recs[i]
		}
	}
	var hit *Rec
	for i := range recs {
		s := recs[i].Section
		last := s
		if i := strings.LastIndex(s, " › "); i >= 0 {
			last = s[i+len(" › "):]
		}
		if strings.EqualFold(s, name) || strings.EqualFold(last, name) {
			if hit != nil {
				return nil // ambiguous: a claim that does not resolve
			}
			hit = &recs[i]
		}
	}
	return hit
}

func squash(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// ownerOf is the record that states st (by id, else by section).
func ownerOf(recs []Rec, st Stmt) *Rec {
	for i := range recs {
		if st.Own != "" && recs[i].ID == st.Own {
			return &recs[i]
		}
		if st.Own == "" && recs[i].ID == "" && recs[i].Section == st.Section {
			return &recs[i]
		}
	}
	return nil
}

// Validate checks answers against the changed statements and the records and
// sets each answer's Status and Detail. It returns the answers in the order of
// stmts (then any stale ones), with a missing answer filled in as UNANSWERED,
// and whether every one is ok. root, when non-empty, is the code tree: a
// location's identifier must occur in the target's file if that file exists.
// Nothing the answers' own Status or Text fields say is trusted.
func Validate(stmts []Stmt, recs []Rec, answers []Answer, root string) ([]Answer, bool) {
	byHash := map[string][]Answer{}
	for _, a := range answers {
		byHash[a.Hash] = append(byHash[a.Hash], a)
	}
	var out []Answer
	allOK := true
	seen := map[string]bool{}
	for _, st := range stmts {
		seen[st.Hash] = true
		as := byHash[st.Hash]
		switch {
		case len(as) == 0:
			out = append(out, Answer{Section: st.Section, Hash: st.Hash, Text: st.Text, Status: Unanswered, Detail: "no answer for this changed statement"})
			allOK = false
		case len(as) > 1:
			a := as[0]
			a.Section, a.Text = st.Section, st.Text
			a.Status, a.Detail = Dangling, fmt.Sprintf("%d answers for one statement", len(as))
			out = append(out, a)
			allOK = false
		default:
			a := as[0]
			a.Section = st.Section
			if a.Text != st.Text { // the quote is re-derived from the spec, never trusted
				a.Status, a.Detail = Dangling, "quoted text is not the spec's statement"
				a.Text = st.Text
			} else {
				a.Status, a.Detail = check(st, a, recs, root)
			}
			if a.Status != OK {
				allOK = false
			}
			out = append(out, a)
		}
	}
	var stale []Answer
	for _, a := range answers {
		if !seen[a.Hash] {
			a.Status, a.Detail = Stale, "not a changed statement of this spec (the spec moved since the proposal)"
			stale = append(stale, a)
			allOK = false
		}
	}
	sort.SliceStable(stale, func(i, j int) bool { return stale[i].Hash < stale[j].Hash })
	return append(out, stale...), allOK
}

func check(st Stmt, a Answer, recs []Rec, root string) (string, string) {
	switch a.Verdict {
	case Local:
		if len(a.Targets) > 0 {
			return Dangling, "verdict local with targets"
		}
		return OK, ""
	case Constrains:
		if len(a.Targets) == 0 {
			return Dangling, "verdict constrains names no target"
		}
	default:
		return Dangling, fmt.Sprintf("unknown verdict %q (want %q or %q)", a.Verdict, Local, Constrains)
	}
	own := ownerOf(recs, st)
	seen := map[*Rec]bool{}
	for _, t := range a.Targets {
		r := Resolve(recs, t.Record)
		if r == nil {
			return Dangling, fmt.Sprintf("target %q is not a record or section", t.Record)
		}
		if r == own {
			return Dangling, fmt.Sprintf("target %q is the statement's own record (that verdict is %q)", t.Record, Local)
		}
		if seen[r] {
			return Dangling, fmt.Sprintf("target %q named twice", t.Record)
		}
		seen[r] = true
		if strings.TrimSpace(t.Reason) == "" {
			return Dangling, fmt.Sprintf("target %q has no reason", t.Record)
		}
		if d := checkLocation(r, t.Location, root); d != "" {
			return Dangling, fmt.Sprintf("target %q: %s", t.Record, d)
		}
		if r.ID != "" { // a New section has no record text to quote yet
			hay := squash(r.Intent + " " + strings.Join(r.Constraints, " "))
			q := squash(t.Quote)
			if q == "" || !strings.Contains(hay, q) {
				return Dangling, fmt.Sprintf("target %q: quote %q does not occur in that record", t.Record, t.Quote)
			}
		}
	}
	return OK, ""
}

func checkLocation(r *Rec, loc, root string) string {
	file, ident, _ := strings.Cut(loc, ":")
	file, ident = strings.TrimSpace(file), strings.TrimSpace(ident)
	if file == "" {
		return "no location (want \"file: Identifier\")"
	}
	if r.File != "" && filepath.ToSlash(filepath.Clean(file)) != r.File {
		return fmt.Sprintf("location file %q is not the record's file %q", file, r.File)
	}
	if root != "" && r.File != "" && ident != "" {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.File)))
		if err == nil && !strings.Contains(string(b), ident) {
			return fmt.Sprintf("identifier %q does not occur in %s", ident, r.File)
		}
	}
	return ""
}

// Input is everything one proposal draws on.
type Input struct {
	Stmts       []Stmt
	Recs        []Rec
	Conventions string
	Code        map[string]string // optional: file -> current source (--with-code)
	Root        string            // optional: the code tree, for location checks
	Retries     int               // times an unparseable answer goes back to the model
	Strict      bool              // an invalid answer is a retry too, not only an unparseable one
}

// ErrNoProposal: no parseable answer within the retries.
var ErrNoProposal = errors.New("crossref: no valid proposal")

// Propose asks the model once per attempt and returns the validated proposal and
// the number of model calls made. Only an unparseable answer (or, with Strict,
// an invalid one) is retried; a proposal with UNANSWERED or DANGLING entries is
// returned as it is so a human sees exactly what was wrong.
func Propose(ctx context.Context, m model.Model, in Input) (Proposal, int, error) {
	prompt := Prompt(in)
	calls := 0
	var last Proposal
	for attempt := 0; attempt <= in.Retries; attempt++ {
		calls++
		resp, err := m.Complete(ctx, prompt)
		if err != nil {
			return Proposal{}, calls, err
		}
		answers, perr := Parse(resp, in.Stmts)
		if perr != nil {
			prompt = Prompt(in) + "\n## Your previous answer was refused\n\n" + perr.Error() + "\n\nAnswer again with one JSON object in a single ```json block.\n"
			continue
		}
		checked, ok := Validate(in.Stmts, in.Recs, answers, in.Root)
		last = Proposal{Schema: SchemaVersion, Answers: checked}
		if ok || !in.Strict {
			return last, calls, nil
		}
		var bad []string
		for _, a := range checked {
			if a.Status != OK {
				bad = append(bad, fmt.Sprintf("- %q: %s: %s", a.Text, a.Status, a.Detail))
			}
		}
		prompt = Prompt(in) + "\n## Your previous answer was refused\n\n" + strings.Join(bad, "\n") + "\n\nAnswer again with one JSON object in a single ```json block.\n"
	}
	if len(last.Answers) > 0 {
		return last, calls, nil
	}
	return Proposal{}, calls, fmt.Errorf("%w within %d attempts", ErrNoProposal, in.Retries+1)
}

type rawAnswer struct {
	ID      string   `json:"id"`
	Verdict string   `json:"verdict"`
	Targets []Target `json:"targets"`
}

type rawProposal struct {
	Statements []rawAnswer `json:"statements"`
}

// Parse reads the model's answer: one JSON object (the last ```json block, else
// the whole reply) with an entry per statement id. Unknown keys, trailing data
// and unknown ids are refused; the answers come back keyed to the statements'
// hashes and with the statement text quoted from stmts.
func Parse(resp string, stmts []Stmt) ([]Answer, error) {
	body := lastFence(resp)
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.DisallowUnknownFields()
	var rp rawProposal
	if err := dec.Decode(&rp); err != nil {
		return nil, fmt.Errorf("not the requested JSON object: %v", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON object")
	}
	byID := map[string]Stmt{}
	for i, s := range stmts {
		byID[ID(i)] = s
	}
	var out []Answer
	for _, r := range rp.Statements {
		s, ok := byID[r.ID]
		if !ok {
			return nil, fmt.Errorf("unknown statement id %q", r.ID)
		}
		out = append(out, Answer{Section: s.Section, Hash: s.Hash, Text: s.Text, Verdict: r.Verdict, Targets: r.Targets})
	}
	return out, nil
}

// ID is the short id the prompt gives the i-th statement.
func ID(i int) string { return fmt.Sprintf("s%d", i+1) }

func lastFence(s string) string {
	const open = "```"
	i := strings.LastIndex(s, open)
	for i >= 0 {
		// find the block that ends at i: look for its opening fence before it
		j := strings.LastIndex(s[:i], open)
		if j < 0 {
			break
		}
		block := s[j+len(open) : i]
		if nl := strings.Index(block, "\n"); nl >= 0 && (strings.TrimSpace(block[:nl]) == "" || strings.TrimSpace(block[:nl]) == "json") {
			return block[nl+1:]
		}
		i = strings.LastIndex(s[:j], open)
	}
	return strings.TrimSpace(s)
}

// Prompt is what the model sees: the changed statements, every record with its
// intent, constraints, file and imports, the conventions and, optionally, code.
// The marker line is what a test double keys on.
func Prompt(in Input) string {
	var b strings.Builder
	b.WriteString("You are doing a CROSS-REFERENCE check on a spec change (graft crossref).\n\n")
	b.WriteString("A spec is generated section by section: each section has one record that owns one code file. A statement in one section can constrain the behaviour of code that ANOTHER section's record owns, without naming it. For each changed statement below decide:\n")
	b.WriteString("- \"local\": the statement is entirely about its own section's behaviour;\n")
	b.WriteString("- \"constrains\": the statement also binds the behaviour of one or more OTHER records, whose code must change to satisfy it.\n")
	b.WriteString("Do not match words; decide by what the code of each record must do. Prefer \"local\" unless some other record's code must change.\n\n")
	if c := strings.TrimSpace(in.Conventions); c != "" {
		fmt.Fprintf(&b, "## Conventions\n\n%s\n\n", c)
	}
	b.WriteString("## Changed statements\n\n")
	for i, s := range in.Stmts {
		fmt.Fprintf(&b, "- id %s, section %q, [%s]: %s\n", ID(i), s.Section, s.Type, s.Text)
	}
	b.WriteString("\n## Records\n\n")
	for _, r := range in.Recs {
		name := r.ID
		if name == "" {
			name = r.Section + " (new section, no record yet)"
		}
		fmt.Fprintf(&b, "### record %s — %s\n", name, r.Section)
		if r.File != "" {
			fmt.Fprintf(&b, "file: %s\n", r.File)
		}
		if r.Intent != "" {
			fmt.Fprintf(&b, "intent:\n%s\n", r.Intent)
		}
		for _, c := range r.Constraints {
			fmt.Fprintf(&b, "constraint: %s\n", c)
		}
		for _, c := range r.Imports {
			fmt.Fprintf(&b, "imported: %s\n", c)
		}
		if src, ok := in.Code[r.File]; ok && r.File != "" {
			fmt.Fprintf(&b, "current code of %s:\n```\n%s\n```\n", r.File, strings.TrimRight(src, "\n"))
		}
		b.WriteString("\n")
	}
	b.WriteString("## Answer\n\nAnswer with ONE JSON object in a single ```json block, exactly this shape, one entry per statement id:\n\n")
	b.WriteString("```json\n{\"statements\": [\n  {\"id\": \"s1\", \"verdict\": \"local\"},\n  {\"id\": \"s2\", \"verdict\": \"constrains\", \"targets\": [\n    {\"record\": \"<record id, or the section heading for a new section>\",\n     \"reason\": \"why this statement binds that record's behaviour\",\n     \"location\": \"<that record's file>: <function or stub that must change>\",\n     \"quote\": \"<a phrase copied verbatim from that record's intent or constraints>\"}]}\n]}\n```\n")
	return b.String()
}

// Marker is the first line of every Prompt; a test double keys on it.
const Marker = "You are doing a CROSS-REFERENCE check"

var (
	reTick  = regexp.MustCompile("`([^`]+)`")
	reCaps  = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)
	reNum   = regexp.MustCompile(`\b\d{3}\b`)
	reCamel = regexp.MustCompile(`\b[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*\b`)
	stopTok = map[string]bool{"HTTP": true, "JSON": true, "API": true, "URL": true, "UTF": true, "TCP": true, "THE": true, "AND": true, "NOT": true}
)

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reTick.FindAllStringSubmatch(s, -1) {
		out[strings.TrimSpace(m[1])] = true
	}
	for _, re := range []*regexp.Regexp{reCaps, reNum, reCamel} {
		for _, m := range re.FindAllString(s, -1) {
			if !stopTok[m] {
				out[m] = true
			}
		}
	}
	return out
}

// Warn is the model-free advisory signal: a statement judged "local" (or
// answered with targets that leave a name out) that names a command, status
// code or identifier which another record's text also uses. It is a hint for
// the human reading the local verdicts; it never changes an answer or an exit
// code, and token matching alone is not enough (SPEC §12), so it is neither
// complete nor precise.
func Warn(stmts []Stmt, answers []Answer, recs []Rec) []string {
	byHash := map[string]Answer{}
	for _, a := range answers {
		byHash[a.Hash] = a
	}
	var out []string
	for _, st := range stmts {
		a, ok := byHash[st.Hash]
		if !ok || a.Status != OK || a.Verdict != Local {
			continue
		}
		own := ownerOf(recs, st)
		toks := tokens(st.Text)
		var names []string
		for t := range toks {
			names = append(names, t)
		}
		sort.Strings(names)
		for _, t := range names {
			var where []string
			for i := range recs {
				if &recs[i] == own {
					continue
				}
				if tokens(recs[i].Intent + "\n" + strings.Join(recs[i].Constraints, "\n"))[t] {
					where = append(where, recs[i].Section)
				}
			}
			if len(where) > 0 {
				out = append(out, fmt.Sprintf("advisory: %q (%s) was judged local but names %s, which %s also mentions", st.Text, st.Section, t, strings.Join(where, ", ")))
			}
		}
	}
	return out
}
