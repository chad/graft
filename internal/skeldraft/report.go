package skeldraft

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Report renders the coverage report. It says what it is: the model's claim.
// The only hard signal is an UNOWNED line.
func (r Result) Report() string {
	var b strings.Builder
	b.WriteString("coverage (the model's claim, not verified; an UNOWNED statement is the hard signal)\n")
	section := ""
	for _, o := range r.Coverage {
		if o.Statement.Section != section {
			section = o.Statement.Section
			fmt.Fprintf(&b, "%s\n", section)
		}
		text := o.Statement.Text
		if len(text) > 72 {
			text = text[:69] + "..."
		}
		fmt.Fprintf(&b, "  %-4s %s\n", o.Statement.ID, text)
		if o.Unowned() {
			fmt.Fprintf(&b, "       -> UNOWNED\n")
		} else {
			fmt.Fprintf(&b, "       -> %s [claimed]\n", strings.Join(o.Claimed, ", "))
		}
		if len(o.Dangling) > 0 {
			fmt.Fprintf(&b, "       -> DANGLING claim, names nothing in the proposal: %s\n", strings.Join(o.Dangling, ", "))
		}
	}
	if d := r.Dangling(); len(d) > 0 {
		fmt.Fprintf(&b, "DANGLING: %d statement(s) with a claim naming nothing in the proposal: %s\n", len(d), danglingList(d))
	}
	if u := r.Unowned(); len(u) > 0 {
		fmt.Fprintf(&b, "UNOWNED: %d of %d statement(s): %s\n", len(u), len(r.Coverage), ids(u))
	} else {
		fmt.Fprintf(&b, "UNOWNED: none (of %d statements; the mapping itself is still the model's claim)\n", len(r.Coverage))
	}
	return b.String()
}

// ReportJSON is the same report as data, written beside the proposal.
func (r Result) ReportJSON() ([]byte, error) {
	type row struct {
		ID      string   `json:"id"`
		Section string   `json:"section"`
		Type    string   `json:"type"`
		Text    string   `json:"text"`
		Claimed []string `json:"claimed_owners"`
		Ignored []string `json:"dangling_claims,omitempty"`
		Unowned bool     `json:"unowned"`
	}
	out := struct {
		Note       string `json:"note"`
		Statements []row  `json:"statements"`
		Unowned    int    `json:"unowned"`
		Dangling   int    `json:"dangling"`
	}{Note: "claimed_owners are the drafting model's claim, not evidence; unowned is the hard signal"}
	for _, o := range r.Coverage {
		c := o.Claimed
		if c == nil {
			c = []string{}
		}
		out.Statements = append(out.Statements, row{o.Statement.ID, o.Statement.Section, string(o.Statement.Type), o.Statement.Text, c, o.Dangling, o.Unowned()})
	}
	out.Unowned, out.Dangling = len(r.Unowned()), len(r.Dangling())
	return json.MarshalIndent(out, "", "  ")
}
