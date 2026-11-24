package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/crossref"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/records"
)

const xSpec1 = "# T\n\n## Alpha\n\n- Alpha must record every event it sees\n\n## Beta\n\n- Beta must count the events it is told about\n"

// the Alpha rule concerns Beta's code without naming Beta
const xRule = "Every event must also reach the counters before the reply is sent"

const xSpec2 = "# T\n\n## Alpha\n\n- Alpha must record every event it sees\n- " + xRule + "\n\n## Beta\n\n- Beta must count the events it is told about\n"

// fakeXref answers the cross-reference prompt: the statement containing rule
// constrains section target (by id when byID, else by heading); the rest is local.
type fakeXref struct {
	rule, target string
	byID         bool
	calls        int
}

func (f *fakeXref) Complete(_ context.Context, prompt string) (string, error) {
	f.calls++
	recs := map[string][3]string{} // last part of section -> id, first intent phrase, file
	for _, m := range regexp.MustCompile(`(?s)### record (\S+) — ([^\n]+)\n(?:file: ([^\n]+)\n)?intent:\n[^\n]*\n- \[\w+\] ([^\n]+)`).FindAllStringSubmatch(prompt, -1) {
		sec := m[2][strings.LastIndex(m[2], " › ")+1:]
		recs[strings.TrimSpace(strings.TrimPrefix(sec, "›"))] = [3]string{m[1], m[4], m[3]}
	}
	var ents []string
	for _, m := range regexp.MustCompile(`- id (s\d+), section "[^"]+", \[\w+\]: ([^\n]+)`).FindAllStringSubmatch(prompt, -1) {
		if !strings.Contains(m[2], f.rule) {
			ents = append(ents, `{"id":"`+m[1]+`","verdict":"local"}`)
			continue
		}
		name, quote, file := f.target, "", strings.ToLower(f.target)+".go" // a section that is new: its derived file
		if r, ok := recs[f.target]; ok {
			quote, file = r[1], r[2]
			if f.byID {
				name = r[0]
			}
		}
		q := strings.ReplaceAll(quote, `"`, `\"`)
		ents = append(ents, `{"id":"`+m[1]+`","verdict":"constrains","targets":[{"record":"`+name+`","reason":"the rule binds its code","location":"`+file+`: Count","quote":"`+q+`"}]}`)
	}
	return "```json\n{\"statements\":[" + strings.Join(ents, ",") + "]}\n```\n", nil
}

func xAccept(t *testing.T, repo, spec string, cx *CrossrefOptions) ([]Accepted, error) {
	t.Helper()
	return AcceptWith(repo, drafter.Drafts(spec), "test", time.Now(), AcceptOptions{Ext: ".go", Crossref: cx})
}

func xProposal(t *testing.T, repo, spec string, m *fakeXref) crossref.Proposal {
	t.Helper()
	p, _, _, _, err := Crossref(context.Background(), CrossrefInput{Repo: repo, Drafts: drafter.Drafts(spec), Model: m})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func xRecord(t *testing.T, repo, section string) (string, Entry) {
	t.Helper()
	idx, err := LoadIndex(repo)
	if err != nil {
		t.Fatal(err)
	}
	for id, e := range idx.Records {
		if strings.HasSuffix(e.Section, section) {
			return id, e
		}
	}
	t.Fatalf("no record for %s", section)
	return "", Entry{}
}

func xVersion(t *testing.T, repo, id string) int {
	t.Helper()
	st, err := records.OpenDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.Current(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Version
}

func xIntent(t *testing.T, repo, section string) string {
	t.Helper()
	id, _ := xRecord(t, repo, section)
	st, _ := records.OpenDir(repo)
	b, err := st.Snapshot(id, xVersion(t, repo, id))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCrossrefFirstAcceptNeedsNoAnswer(t *testing.T) {
	repo := t.TempDir()
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, xSpec1, &CrossrefOptions{Report: rep}); err != nil {
		t.Fatal(err)
	}
	if rep.Skipped == "" || rep.Changed != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestCrossrefChangedStatementWithoutAnswerRefusesAndWritesNothing(t *testing.T) {
	repo := t.TempDir()
	if _, err := xAccept(t, repo, xSpec1, nil); err != nil {
		t.Fatal(err)
	}
	id, _ := xRecord(t, repo, "Alpha")
	v := xVersion(t, repo, id)
	_, err := xAccept(t, repo, xSpec2, &CrossrefOptions{})
	if !errors.Is(err, ErrCrossref) || !strings.Contains(err.Error(), "UNANSWERED") || !strings.Contains(err.Error(), xRule) {
		t.Fatalf("%v", err)
	}
	if xVersion(t, repo, id) != v {
		t.Fatal("a refused acceptance wrote a version")
	}
	// the library without the option behaves as before
	if _, err := xAccept(t, repo, xSpec2, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCrossrefAcceptCopiesTheStatementIntoTheOwner(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	betaID, _ := xRecord(t, repo, "Beta")
	v := xVersion(t, repo, betaID)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	rep := &CrossrefReport{}
	acc, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop, Report: rep})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Accepted) != 1 || rep.Changed != 1 || len(rep.Local) != 0 {
		t.Fatalf("%+v", rep)
	}
	if xVersion(t, repo, betaID) != v+1 {
		t.Fatalf("Beta must be revised: version %d -> %d", v, xVersion(t, repo, betaID))
	}
	if !strings.Contains(xIntent(t, repo, "Beta"), xRule) {
		t.Fatal("the rule is not in Beta's record")
	}
	var beta Accepted
	for _, a := range acc {
		if a.ID == betaID {
			beta = a
		}
	}
	found := false
	for _, s := range beta.Draft.Statements {
		found = found || strings.Contains(s.Text, xRule)
	}
	if !found {
		t.Fatal("the model prompt for Beta would not carry the rule")
	}
	_, e := xRecord(t, repo, "Beta")
	if len(e.Imports) != 1 || e.Imports[0].FromHash == "" || len(e.DerivedFrom) != 1 {
		t.Fatalf("the import is a link, never DerivedFrom: %+v", e)
	}
	// Alpha's own record keeps the rule too (it is stated there) and is not an import.
	_, ea := xRecord(t, repo, "Alpha")
	if len(ea.Imports) != 0 || len(ea.DerivedFrom) != 2 {
		t.Fatalf("%+v", ea)
	}

	// Re-running the same spec: nothing is changed, nothing to answer, no new version, the import stays.
	rep = &CrossrefReport{}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Report: rep}); err != nil || rep.Changed != 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	if xVersion(t, repo, betaID) != v+1 {
		t.Fatal("an unchanged re-run minted a version")
	}
	if _, e = xRecord(t, repo, "Beta"); len(e.Imports) != 1 {
		t.Fatalf("the import must stay while its source stands: %+v", e)
	}
}

func TestCrossrefImportFollowsItsSource(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	betaID, _ := xRecord(t, repo, "Beta")
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop}); err != nil {
		t.Fatal(err)
	}
	v := xVersion(t, repo, betaID)

	// the source is edited: the old copy goes, the new text is a changed statement to answer again
	const edited = "Every event must reach the counters before the reply is sent and before it is logged"
	spec3 := strings.Replace(xSpec2, xRule, edited, 1)
	if _, err := xAccept(t, repo, spec3, &CrossrefOptions{}); !errors.Is(err, ErrCrossref) {
		t.Fatalf("an edited source is a changed statement: %v", err)
	}
	prop3 := xProposal(t, repo, spec3, &fakeXref{rule: edited, target: "Beta", byID: true})
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, spec3, &CrossrefOptions{Proposal: &prop3, Report: rep}); err != nil {
		t.Fatal(err)
	}
	if len(rep.Dropped) != 1 || len(rep.Accepted) != 1 {
		t.Fatalf("%+v", rep)
	}
	in := xIntent(t, repo, "Beta")
	if strings.Contains(in, xRule+" (") || !strings.Contains(in, edited) {
		t.Fatalf("the old copy must be replaced by the new one:\n%s", in)
	}
	if _, e := xRecord(t, repo, "Beta"); len(e.Imports) != 1 {
		t.Fatalf("one live copy, no duplicate: %+v", e.Imports)
	}
	if xVersion(t, repo, betaID) != v+1 {
		t.Fatalf("version %d", xVersion(t, repo, betaID))
	}

	// the source is removed: the copy goes with it, and the owner is revised
	rep = &CrossrefReport{}
	if _, err := xAccept(t, repo, xSpec1, &CrossrefOptions{Report: rep}); err != nil {
		t.Fatal(err)
	}
	if len(rep.Dropped) != 1 {
		t.Fatalf("%+v", rep)
	}
	if _, e := xRecord(t, repo, "Beta"); len(e.Imports) != 0 {
		t.Fatalf("%+v", e.Imports)
	}
	if strings.Contains(xIntent(t, repo, "Beta"), "counters before the reply") {
		t.Fatal("the removed rule is still in Beta's record")
	}
}

func TestCrossrefLocalVerdictIsReportedNotImported(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: "no such statement"}) // everything local
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop, Report: rep}); err != nil {
		t.Fatal(err)
	}
	if len(rep.Local) != 1 || !strings.Contains(rep.Local[0], xRule) || len(rep.Accepted) != 0 {
		t.Fatalf("%+v", rep)
	}
	if _, e := xRecord(t, repo, "Beta"); len(e.Imports) != 0 {
		t.Fatal("a local verdict must import nothing")
	}
}

func TestCrossrefWaiverIsCountedAndExclusiveWithAProposal(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta"})
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Waive: true, Proposal: &prop}); err == nil {
		t.Fatal("a proposal and the waiver together must be refused")
	}
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Waive: true, Report: rep}); err != nil {
		t.Fatal(err)
	}
	if rep.Waived != 1 || rep.Unchecked() != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestCrossrefHandEditedDanglingTargetIsRefusedAtAccept(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	for i := range prop.Answers {
		for j := range prop.Answers[i].Targets {
			prop.Answers[i].Targets[j].Record = "prov-0000-nope"
		}
		prop.Answers[i].Status = crossref.OK // a hand-written status is not believed
	}
	_, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop})
	if !errors.Is(err, ErrCrossref) || !strings.Contains(err.Error(), "DANGLING") {
		t.Fatalf("%v", err)
	}
}

func TestCrossrefTargetMayNameANewSection(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	spec := xSpec1 + "\n## Gamma\n\n- Gamma must keep its own tally of events\n"
	spec = strings.Replace(spec, "- Alpha must record every event it sees", "- Alpha must record every event it sees\n- "+xRule, 1)
	prop := xProposal(t, repo, spec, &fakeXref{rule: xRule, target: "Gamma"})
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, spec, &CrossrefOptions{Proposal: &prop, Report: rep}); err != nil {
		t.Fatalf("%v\n%+v", err, prop.Answers)
	}
	gid, ge := xRecord(t, repo, "Gamma")
	if len(ge.Imports) != 1 || !strings.Contains(xIntent(t, repo, "Gamma"), xRule) {
		t.Fatalf("%s %+v", gid, ge)
	}
	// Gamma's own statement is a changed one too (a New section): it was answered
	if rep.Changed != 2 {
		t.Fatalf("changed %d, want the rule and Gamma's own statement", rep.Changed)
	}
}

func TestCrossrefMakesNoModelCallWhenNothingChanged(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	m := &fakeXref{}
	p, stmts, _, calls, err := Crossref(context.Background(), CrossrefInput{Repo: repo, Drafts: drafter.Drafts(xSpec1), Model: m})
	if err != nil || calls != 0 || m.calls != 0 || len(stmts) != 0 || len(p.Answers) != 0 {
		t.Fatalf("%v %d %v", err, calls, stmts)
	}
}

// The freeq-lite #14 probe as a second case: a Topics statement about what a
// joiner receives never names JOIN, which Channels owns.
func TestCrossrefFreeqLiteJoinProbeReachesChannels(t *testing.T) {
	v1 := readExample(t, "freeq-lite/spec/freeq-lite.md")
	const probe = "A joiner of a channel that has a topic receives `:freeq-lite 332 <nick> <channel> :<topic>` before the 353 line"
	v2 := v1 + "\n## Topics\n\n- `TOPIC <channel> :<text>` must set the channel's topic\n- " + probe + "\n"
	repo := t.TempDir()
	if _, err := xAccept(t, repo, v1, nil); err != nil {
		t.Fatal(err)
	}
	chID, _ := xRecord(t, repo, "Channels")
	v := xVersion(t, repo, chID)

	// nothing answered: the change is refused, the dropped rule can no longer report success
	if _, err := xAccept(t, repo, v2, &CrossrefOptions{}); !errors.Is(err, ErrCrossref) || !strings.Contains(err.Error(), "332") {
		t.Fatalf("%v", err)
	}

	// a model that says "local" is accepted, printed, and warned about without a model
	local := &fakeXref{rule: "no such statement"}
	pl, stmts, recs, _, err := Crossref(context.Background(), CrossrefInput{Repo: repo, Drafts: drafter.Drafts(v2), Model: local})
	if err != nil {
		t.Fatal(err)
	}
	warns := crossref.Warn(stmts, pl.Answers, recs)
	if !strings.Contains(strings.Join(warns, "\n"), "Channels") {
		t.Fatalf("the model-free warning should point the human at Channels: %v", warns)
	}

	// a model that understood: constrains Channels
	prop := xProposal(t, repo, v2, &fakeXref{rule: probe, target: "Channels", byID: true})
	rep := &CrossrefReport{}
	acc, err := xAccept(t, repo, v2, &CrossrefOptions{Proposal: &prop, Report: rep})
	if err != nil {
		t.Fatalf("%v\n%+v", err, prop.Answers)
	}
	if xVersion(t, repo, chID) != v+1 || !strings.Contains(xIntent(t, repo, "Channels"), "332") {
		t.Fatal("Channels must be revised with the probe statement")
	}
	for _, a := range acc {
		if a.ID == chID {
			ok := false
			for _, s := range a.Draft.Statements {
				ok = ok || strings.Contains(s.Text, "332")
			}
			if !ok {
				t.Fatal("Channels' region would be regenerated without the statement")
			}
		}
	}
}

func readExample(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../examples/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- review fixes (#43) ----

func gammaSpec(extra map[string]string) string {
	// Alpha, Beta, Gamma; extra maps a section name to a line appended to it
	sec := func(name, line string) string {
		s := "## " + name + "\n\n- " + line + "\n"
		if e := extra[name]; e != "" {
			s += "- " + e + "\n"
		}
		return s + "\n"
	}
	return "# T\n\n" + sec("Alpha", "Alpha must record every event it sees") + sec("Beta", "Beta must count the events it is told about") + sec("Gamma", "Gamma must keep its own tally of events")
}

// The same sentence in two sections is two statements with two answers.
func TestCrossrefIdenticalStatementsInTwoSectionsAreTwoAnswers(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, gammaSpec(nil), nil)
	const dup = "Every reply must be logged"
	spec := gammaSpec(map[string]string{"Alpha": dup, "Beta": dup})
	prop := xProposal(t, repo, spec, &fakeXref{rule: dup, target: "Gamma", byID: true})
	if len(prop.Answers) != 2 {
		t.Fatalf("%d answers: %+v", len(prop.Answers), prop.Answers)
	}
	for _, a := range prop.Answers {
		if a.Status != "ok" {
			t.Fatalf("%+v", a)
		}
	}
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, spec, &CrossrefOptions{Proposal: &prop, Report: rep}); err != nil {
		t.Fatal(err)
	}
	_, e := xRecord(t, repo, "Gamma")
	if len(e.Imports) != 2 || e.Imports[0].FromRecord == e.Imports[1].FromRecord {
		t.Fatalf("one import per source section, not doubled: %+v", e.Imports)
	}
}

// Renaming the source section keeps its imports: the source is a record id.
func TestCrossrefImportSurvivesRenamingItsSourceSection(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop}); err != nil {
		t.Fatal(err)
	}
	betaID, _ := xRecord(t, repo, "Beta")
	v := xVersion(t, repo, betaID)
	renamed := strings.Replace(xSpec2, "## Alpha", "## Alpha events", 1)
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, renamed, &CrossrefOptions{Report: rep}); err != nil {
		t.Fatalf("a rename is not a change that needs answers: %v", err)
	}
	_, e := xRecord(t, repo, "Beta")
	if len(e.Imports) != 1 || len(rep.Dropped) != 0 || rep.Changed != 0 {
		t.Fatalf("%+v %+v", e.Imports, rep)
	}
	if e.Imports[0].FromSection != "T › Alpha events" {
		t.Fatalf("the display heading follows the rename: %q", e.Imports[0].FromSection)
	}
	if !strings.Contains(xIntent(t, repo, "Beta"), xRule) {
		t.Fatal("the rule left Beta's record")
	}
	_ = v
}

// An import dropped for any reason other than its source going is re-asked:
// here the index's source record is corrupted, so the rule would silently leave
// Beta; instead the statement counts as changed and accept refuses until answered.
func TestCrossrefForcedDropIsReAskedNeverSilent(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop}); err != nil {
		t.Fatal(err)
	}
	betaID, _ := xRecord(t, repo, "Beta")
	idx, _ := LoadIndex(repo)
	e := idx.Records[betaID]
	e.Imports[0].FromRecord = "prov-0000-gone"
	idx.Records[betaID] = e
	if err := idx.save(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{}); !errors.Is(err, ErrCrossref) || !strings.Contains(err.Error(), xRule) {
		t.Fatalf("the orphaned import's statement must be re-asked: %v", err)
	}
	prop2 := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	rep := &CrossrefReport{}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop2, Report: rep}); err != nil {
		t.Fatal(err)
	}
	_, e = xRecord(t, repo, "Beta")
	alphaID, _ := xRecord(t, repo, "Alpha")
	if len(e.Imports) != 1 || e.Imports[0].FromRecord != alphaID || len(rep.Dropped) != 1 {
		t.Fatalf("%+v %+v", e.Imports, rep)
	}
}

// A New target's location is checked against the file accept will place,
// including when a "files" config moves it.
func TestCrossrefNewSectionLocationFollowsTheFilesConfig(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	spec := xSpec1 + "\n## Gamma\n\n- Gamma must keep its own tally of events\n"
	spec = strings.Replace(spec, "- Alpha must record every event it sees", "- Alpha must record every event it sees\n- "+xRule, 1)
	opts := AcceptOptions{Ext: ".go", Files: map[string]string{"Gamma": "tally/tally.go"}}
	_, recs, err := CrossrefState(repo, drafter.Drafts(spec), nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	var g *struct{ File string }
	for _, r := range recs {
		if strings.HasSuffix(r.Section, "Gamma") {
			g = &struct{ File string }{r.File}
		}
	}
	if g == nil || g.File != "tally/tally.go" {
		t.Fatalf("a New target's file is the placed one: %+v", g)
	}
	m := &fakeXref{rule: xRule, target: "Gamma"}
	p, _, _, _, err := Crossref(context.Background(), CrossrefInput{Repo: repo, Drafts: drafter.Drafts(spec), Model: m, Accept: opts})
	if err != nil {
		t.Fatal(err)
	}
	// fakeXref's location for a New target is "gamma.go: Count": not the placed file
	bad := false
	for _, a := range p.Answers {
		bad = bad || (a.Status == "DANGLING" && strings.Contains(a.Detail, "tally/tally.go"))
	}
	if !bad {
		t.Fatalf("x.go is not Gamma's placed file: %+v", p.Answers)
	}
	for i := range p.Answers {
		for j := range p.Answers[i].Targets {
			p.Answers[i].Targets[j].Location = "tally/tally.go: Count"
		}
	}
	// accept re-validates against the same placement: refused without the config, accepted with it
	if _, err := xAccept(t, repo, spec, &CrossrefOptions{Proposal: &p}); !errors.Is(err, ErrCrossref) {
		t.Fatalf("without the files config the location is not Gamma's file: %v", err)
	}
	opts.Crossref = &CrossrefOptions{Proposal: &p}
	if _, err := AcceptWith(repo, drafter.Drafts(spec), "test", time.Now(), opts); err != nil {
		t.Fatal(err)
	}
}

// At accept the identifier is re-checked when the tree is available.
func TestCrossrefAcceptRechecksTheIdentifierAgainstTheTree(t *testing.T) {
	repo, root := t.TempDir(), t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true}) // location "beta.go: Count", no root at proposal time
	_, e := xRecord(t, repo, "Beta")
	os.WriteFile(filepath.Join(root, e.File), []byte("package main\nfunc Other() {}\n"), 0o644)
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop, Root: root}); !errors.Is(err, ErrCrossref) || !strings.Contains(err.Error(), "does not occur") {
		t.Fatalf("%v", err)
	}
	os.WriteFile(filepath.Join(root, e.File), []byte("package main\nfunc Count() {}\n"), 0o644)
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop, Root: root}); err != nil {
		t.Fatal(err)
	}
}

// A quote must cite the target's OWN statements, never text imported into it.
func TestCrossrefQuoteMustBeTheTargetsOwnText(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	prop := xProposal(t, repo, xSpec2, &fakeXref{rule: xRule, target: "Beta", byID: true})
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop}); err != nil {
		t.Fatal(err)
	}
	const rule2 = "Every event must also be timestamped"
	spec3 := strings.Replace(xSpec2, "- "+xRule, "- "+xRule+"\n- "+rule2, 1)
	p3 := xProposal(t, repo, spec3, &fakeXref{rule: rule2, target: "Beta", byID: true})
	for _, a := range p3.Answers {
		if a.Status != "ok" {
			t.Fatalf("a quote of Beta's own statement passes: %+v", a)
		}
	}
	for i := range p3.Answers {
		for j := range p3.Answers[i].Targets {
			p3.Answers[i].Targets[j].Quote = xRule // the text Beta imported from Alpha
		}
	}
	if _, err := xAccept(t, repo, spec3, &CrossrefOptions{Proposal: &p3}); !errors.Is(err, ErrCrossref) || !strings.Contains(err.Error(), "quote") {
		t.Fatalf("%v", err)
	}
}

// proseFirst answers with prose location identifiers for its first `bad` calls
// (what a real model did in the pilot), then delegates to a correct fake.
type proseFirst struct {
	inner      *fakeXref
	bad, calls int
	prompts    []string
}

func (p *proseFirst) Complete(ctx context.Context, prompt string) (string, error) {
	p.calls++
	p.prompts = append(p.prompts, prompt)
	r, err := p.inner.Complete(ctx, prompt)
	if p.calls <= p.bad {
		r = regexp.MustCompile(`"location":"[^"]*"`).ReplaceAllString(r, `"location":"the Count function and the Beta type"`)
	}
	return r, err
}

func TestCrossrefProseLocationIsRetriedThenAcceptSucceeds(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	m := &proseFirst{inner: &fakeXref{rule: xRule, target: "Beta", byID: true}, bad: 1}
	prop, _, _, calls, err := Crossref(context.Background(), CrossrefInput{Repo: repo, Drafts: drafter.Drafts(xSpec2), Model: m, Retries: 2})
	if err != nil || calls != 2 || m.calls != 2 {
		t.Fatalf("calls=%d/%d err=%v", calls, m.calls, err)
	}
	if !strings.Contains(m.prompts[1], "Feedback on your previous attempt") || !strings.Contains(m.prompts[1], "not of the form") {
		t.Fatal("the second prompt does not carry the validation message")
	}
	for _, a := range prop.Answers {
		if a.Status != crossref.OK {
			t.Fatalf("%+v", a)
		}
	}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop, Report: &CrossrefReport{}}); err != nil {
		t.Fatalf("accept refused a retried proposal: %v", err)
	}
}

func TestCrossrefNeverFixedWritesDanglingAndAcceptRefuses(t *testing.T) {
	repo := t.TempDir()
	xAccept(t, repo, xSpec1, nil)
	m := &proseFirst{inner: &fakeXref{rule: xRule, target: "Beta", byID: true}, bad: 99}
	prop, _, _, calls, err := Crossref(context.Background(), CrossrefInput{Repo: repo, Drafts: drafter.Drafts(xSpec2), Model: m, Retries: 2})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	dangling := 0
	for _, a := range prop.Answers {
		if a.Status == crossref.Dangling {
			dangling++
		}
	}
	if dangling != 1 {
		t.Fatalf("want one DANGLING entry: %+v", prop.Answers)
	}
	if _, err := xAccept(t, repo, xSpec2, &CrossrefOptions{Proposal: &prop, Report: &CrossrefReport{}}); err == nil {
		t.Fatal("accept took a proposal with a DANGLING entry")
	}
}
