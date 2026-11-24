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
	// Render is true in render mode (a skeleton exists): a location must then
	// name a stub or decl of this record's file in the skeleton. Symbols are
	// those names ("Name" and "Type.Name"); none means the skeleton has no file
	// for the record.
	Render  bool
	Symbols []string
	// Idents, outside render mode, are the top-level identifiers found in the
	// record's file under --root (see Idents). A hint only; it is not checked.
	Idents []string
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
	// An answer belongs to a statement by (section, clause hash): the same
	// sentence in two sections is two statements, each with its own answer.
	byHash := map[string][]Answer{}
	for _, a := range answers {
		byHash[key(a.Section, a.Hash)] = append(byHash[key(a.Section, a.Hash)], a)
	}
	var out []Answer
	allOK := true
	seen := map[string]bool{}
	for _, st := range stmts {
		seen[key(st.Section, st.Hash)] = true
		as := byHash[key(st.Section, st.Hash)]
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
		if !seen[key(a.Section, a.Hash)] {
			a.Status, a.Detail = Stale, "not a changed statement of this spec (the spec moved since the proposal)"
			stale = append(stale, a)
			allOK = false
		}
	}
	sort.SliceStable(stale, func(i, j int) bool { return stale[i].Hash < stale[j].Hash })
	return append(out, stale...), allOK
}

func key(section, hash string) string { return section + "\x00" + hash }

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
	case "":
		return Unanswered, "no verdict for this changed statement"
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
	file, ident, found := strings.Cut(loc, ":")
	file, ident = strings.TrimSpace(file), strings.TrimSpace(ident)
	if !found || file == "" || ident == "" {
		return fmt.Sprintf("location %q is not of the form \"file: Identifier\"", loc)
	}
	// Refuse a path that climbs or is absolute BEFORE it is cleaned: Clean would
	// quietly resolve "a/../deposits.go" into the record's own file.
	norm := strings.ReplaceAll(file, "\\", "/")
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return fmt.Sprintf("location file %q has a .. segment", file)
		}
	}
	if strings.HasPrefix(norm, "/") {
		return fmt.Sprintf("location file %q is absolute", file)
	}
	if r.File != "" && filepath.ToSlash(filepath.Clean(norm)) != r.File {
		return fmt.Sprintf("location file %q is not the record's file %q", file, r.File)
	}
	if r.Render {
		if len(r.Symbols) == 0 {
			return "the skeleton has no file for this record: " + NeedsSkeleton + " (graft draft --skeleton)"
		}
		if !hasSymbol(r.Symbols, ident) {
			return fmt.Sprintf("%q is not a stub or decl of this record's file in the skeleton: %s (graft draft --skeleton)", ident, NeedsSkeleton)
		}
	}
	// In render mode the skeleton is the authority (a stub new in this change is
	// not in the tree yet); the file under root is checked only without one.
	if !r.Render && root != "" && r.File != "" {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.File)))
		if err == nil && !hasToken(string(b), ident) {
			return fmt.Sprintf("identifier %q does not occur in %s", ident, r.File)
		}
	}
	return ""
}

// hasSymbol reports whether ident names one of symbols, ignoring a trailing
// "()" and a receiver's star ("Ledger.Post", "(*Ledger).Post" and "Post" all name Post).
func hasSymbol(symbols []string, ident string) bool {
	ident = strings.TrimSuffix(strings.TrimSpace(ident), "()")
	ident = strings.NewReplacer("(*", "", "(", "", ")", "", "*", "").Replace(ident)
	if ident == "" {
		return false
	}
	for _, s := range symbols {
		if s == ident {
			return true
		}
	}
	return false
}

// Input is everything one proposal draws on.
type Input struct {
	Stmts       []Stmt
	Recs        []Rec
	Conventions string
	Code        map[string]string // optional: file -> current source (--with-code)
	Root        string            // optional: the code tree, for location checks
	Retries     int               // times an unparseable answer, or one with UNANSWERED/DANGLING entries, goes back to the model
	Strict      bool              // after the retries, an invalid proposal is an error (nothing written), not exit 4
}

// NeedsSkeleton is in the Detail of an answer only a skeleton change can fix;
// Propose does not re-ask those.
const NeedsSkeleton = "needs a skeleton change"

// ErrNoProposal: no valid proposal within the retries.
var ErrNoProposal = errors.New("crossref: no valid proposal")

// Propose asks the model and returns the validated proposal and the number of
// model calls made. An unparseable answer, and an answer with UNANSWERED or
// DANGLING entries (form errors a model can fix), go back to the model with the
// exact messages ("Feedback on your previous attempt"), within in.Retries. When
// the retries are spent, the last proposal is returned as it is so a human sees
// exactly what was wrong (exit 4); with Strict it is an error instead.
func Propose(ctx context.Context, m model.Model, in Input) (Proposal, int, error) {
	base := Prompt(in)
	prompt := base
	calls := 0
	var best Proposal
	bestBad := -1 // non-ok entries of best; -1: no parseable answer yet
	for attempt := 0; attempt <= in.Retries; attempt++ {
		calls++
		resp, err := m.Complete(ctx, prompt)
		if err != nil {
			return Proposal{}, calls, err
		}
		answers, perr := Parse(resp, in.Stmts)
		if perr != nil {
			prompt = base + "\n## Feedback on your previous attempt\n\nYour answer was refused: " + perr.Error() + "\n\nAnswer again with one JSON object in a single ```json block.\n"
			continue
		}
		checked, ok := Validate(in.Stmts, in.Recs, answers, in.Root)
		if ok {
			return Proposal{Schema: SchemaVersion, Answers: checked}, calls, nil
		}
		var bad []string
		nbad := 0
		for _, a := range checked {
			if a.Status == OK {
				continue
			}
			nbad++
			if !strings.Contains(a.Detail, NeedsSkeleton) { // the model cannot fix a missing skeleton stub
				bad = append(bad, fmt.Sprintf("- %q: %s: %s", a.Text, a.Status, a.Detail))
			}
		}
		// Keep the best attempt: a later, worse answer must not replace it.
		if bestBad < 0 || nbad <= bestBad {
			best, bestBad = Proposal{Schema: SchemaVersion, Answers: checked}, nbad
		}
		if len(bad) == 0 { // only entries that need a skeleton change: report them, do not re-ask
			break
		}
		prompt = base + "\n## Feedback on your previous attempt\n\nThese entries were refused; fix exactly these and answer for every statement again:\n\n" + strings.Join(bad, "\n") + "\n\nAnswer again with one JSON object in a single ```json block.\n"
	}
	if bestBad >= 0 && !in.Strict {
		return best, calls, nil
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
		if r.Render {
			fmt.Fprintf(&b, "stubs and decls of its skeleton file (a location must name one of these): %s\n", strings.Join(r.Symbols, ", "))
		}
		if !r.Render && len(r.Idents) > 0 {
			fmt.Fprintf(&b, "top-level identifiers in its file (a location names one of these): %s\n", strings.Join(r.Idents, ", "))
		}
		if src, ok := in.Code[r.File]; ok && r.File != "" {
			fmt.Fprintf(&b, "current code of %s:\n```\n%s\n```\n", r.File, strings.TrimRight(src, "\n"))
		}
		b.WriteString("\n")
	}
	b.WriteString("## Answer\n\nAnswer with ONE JSON object in a single ```json block, exactly this shape, one entry per statement id:\n\n")
	b.WriteString("```json\n{\"statements\": [\n  {\"id\": \"s1\", \"verdict\": \"local\"},\n  {\"id\": \"s2\", \"verdict\": \"constrains\", \"targets\": [\n    {\"record\": \"<record id, or the section heading for a new section>\",\n     \"reason\": \"why this statement binds that record's behaviour\",\n     \"location\": \"<that record's file>: <one identifier>\",\n     \"quote\": \"<a phrase copied verbatim from that record's intent or constraints>\"}]}\n]}\n```\n\n")
	b.WriteString("The location is EXACTLY `<file>: <Identifier>`: the record's file, a colon, then ONE bare identifier (for example `running.go: Post`). Never a sentence, never several names, never backticks or a signature; put the explanation in \"reason\". It is checked mechanically and an entry that does not match is refused. A statement may name several targets: give one entry in \"targets\" per record, each with its own file and identifier (for example one for Deposits and one for Transfers).\n")
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
		byHash[key(a.Section, a.Hash)] = a
	}
	var out []string
	for _, st := range stmts {
		a, ok := byHash[key(st.Section, st.Hash)]
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

var (
	reTopLine  = regexp.MustCompile(`^(?:func\s+(?:\([^)]*\)\s*)?|type\s+|var\s+|const\s+)([A-Za-z_][A-Za-z0-9_]*)`)
	reGroup    = regexp.MustCompile(`^(?:var|const|type)\s*\(\s*$`)
	reGroupLn  = regexp.MustCompile(`^\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)`)
	reGroupEnd = regexp.MustCompile(`^\)`)
)

// blank replaces comments, strings, raw strings and rune literals of a Go
// source text with spaces, keeping the newlines, so a line scan does not see
// names inside them.
func blank(src string) string {
	b := []byte(src)
	for i := 0; i < len(b); {
		end := -1
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			end = i
			for end < len(b) && b[end] != '\n' {
				end++
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			end = i + 2
			for end < len(b) && !(b[end-1] == '*' && b[end] == '/' && end > i+2) {
				end++
			}
			end++
		case b[i] == '`':
			end = i + 1
			for end < len(b) && b[end] != '`' {
				end++
			}
			end++
		case b[i] == '"' || b[i] == '\'':
			q := b[i]
			end = i + 1
			for end < len(b) && b[end] != q && b[end] != '\n' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			end++
		default:
			i++
			continue
		}
		if end > len(b) {
			end = len(b)
		}
		for j := i; j < end; j++ {
			if b[j] != '\n' {
				b[j] = ' '
			}
		}
		i = end
	}
	return string(b)
}

// Idents lists the top-level function, method, type, var and const names of a
// Go source text, by line pattern (graft does not parse target code here), in
// order and without duplicates; grouped var/const/type blocks are included and
// comments and strings are ignored. It is a hint for the prompt; Validate
// never relies on it.
func Idents(src string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "_" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	inGroup, typeGroup, depth := false, false, 0
	for _, ln := range strings.Split(blank(src), "\n") {
		switch {
		case inGroup:
			if depth == 0 && reGroupEnd.MatchString(ln) {
				inGroup = false
				break
			}
			if depth == 0 {
				if m := reGroupLn.FindStringSubmatch(ln); m != nil {
					for _, n := range strings.Split(m[1], ",") {
						add(strings.TrimSpace(n))
						if typeGroup {
							break
						}
					}
				}
			}
			depth += strings.Count(ln, "{") + strings.Count(ln, "(") + strings.Count(ln, "[") - strings.Count(ln, "}") - strings.Count(ln, ")") - strings.Count(ln, "]")
		case reGroup.MatchString(ln):
			inGroup, typeGroup, depth = true, strings.HasPrefix(ln, "type"), 0
		default:
			if m := reTopLine.FindStringSubmatch(ln); m != nil {
				add(m[1])
			}
		}
	}
	return out
}

// hasToken reports whether ident occurs in src as a whole identifier, not as
// part of a longer name ("Dep" is not in "Deposit").
func hasToken(src, ident string) bool {
	re, err := regexp.Compile(`(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(ident) + `(?:$|[^A-Za-z0-9_])`)
	return err == nil && re.MatchString(src)
}
