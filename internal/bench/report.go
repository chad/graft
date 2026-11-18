package bench

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Report writes a case's results: every citable partition with intervals,
// the only comparisons the harness permits (graft vs each control, within one
// fixture, commit and model), every excluded record with its reason, and any
// corrupt log lines.
func Report(w io.Writer, caseName string, recs []Record, attrs []Attribution, corrupt []Corrupt) error {
	parts, excluded, err := Aggregate(recs, attrs)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "## %s\n\n", caseName)
	if len(parts) == 0 {
		fmt.Fprintln(w, "No citable runs.")
	} else {
		fmt.Fprintln(w, "| fixture | commit | model | arm | works | unreachable (not in works) | checks passed (descriptive; checks within a sample are correlated, so no interval) | failures by layer |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|")
		for _, p := range parts {
			fmt.Fprintf(w, "| %.12s | %.12s | %s | %s | %s | %d | %s | %s |\n", p.Fixture, p.Commit, p.Model, p.Arm, p.Works, p.Outcomes[Unreachable], fmt.Sprintf("%d/%d", p.Checks.K, p.Checks.N), layers(p.ByLayer, p.Unattributed))
		}
		fmt.Fprintln(w)
		for _, cmp := range comparisons(parts) {
			if !Permitted(cmp) {
				return fmt.Errorf("bench: refusing to print a comparison outside the permitted sentences: %q", cmp)
			}
			fmt.Fprintln(w, "- "+cmp)
		}
	}
	if costs := changeCosts(recs, excluded); len(costs) > 0 {
		fmt.Fprintln(w, "\nChange cost per sample (citable runs; listed, not rated):")
		for _, line := range costs {
			fmt.Fprintln(w, "- "+line)
		}
	}
	if len(corrupt) > 0 {
		fmt.Fprintf(w, "\nCorrupt log lines, counted in nothing (%d):\n", len(corrupt))
		for _, c := range corrupt {
			fmt.Fprintf(w, "- %s line %d: %s\n", c.File, c.Line, c.Err)
		}
	}
	if len(excluded) > 0 {
		idx := make([]int, 0, len(excluded))
		for i := range excluded {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		fmt.Fprintf(w, "\nShown, counted in nothing (%d):\n", len(idx))
		for _, i := range idx {
			r := recs[i]
			fmt.Fprintf(w, "- %s %s @%.12s: %s\n", r.At.Format("2006-01-02"), r.Arm, r.Commit, excluded[i])
		}
	}
	return nil
}

func layers(m map[Layer]int, unattributed int) string {
	var ks []string
	for l, n := range m {
		ks = append(ks, fmt.Sprintf("%s %d", l, n))
	}
	if unattributed > 0 {
		ks = append(ks, fmt.Sprintf("unattributed %d", unattributed))
	}
	if len(ks) == 0 {
		return "—"
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// comparisons pairs graft with each control inside the same fixture and commit.
func comparisons(parts []Partition) []string {
	type cell struct{ fixture, commit, model string }
	by := map[cell]map[Arm]Partition{}
	var order []cell
	for _, p := range parts {
		c := cell{p.Fixture, p.Commit, p.Model}
		if by[c] == nil {
			by[c] = map[Arm]Partition{}
			order = append(order, c)
		}
		by[c][p.Arm] = p
	}
	var out []string
	pairs := [][2]Arm{
		{ArmGraft, ArmBaseline}, {ArmGraft, ArmIntent},
		{ArmGraftRender, ArmBaseline}, {ArmGraftRender, ArmIntent}, {ArmGraftRender, ArmGraft},
	}
	for _, c := range order {
		for _, pr := range pairs {
			a, okA := by[c][pr[0]]
			b, okB := by[c][pr[1]]
			if okA && okB {
				out = append(out, Compare(string(pr[0]), a.Works, string(pr[1]), b.Works))
			}
		}
	}
	return out
}

// changeCosts lists, per citable evolve record, each sample's change calls and
// preserved files, without turning them into a rate.
func changeCosts(recs []Record, excluded map[int]string) []string {
	var out []string
	for i, r := range recs {
		if _, ex := excluded[i]; ex {
			continue
		}
		var calls, kept, bytes, synth []string
		for _, s := range r.Samples {
			if s.SynthDiffLines != nil {
				synth = append(synth, fmt.Sprint(*s.SynthDiffLines))
			}
			if s.DiffLines != nil {
				bytes = append(bytes, fmt.Sprint(*s.DiffLines))
			}
			if s.ChangeCalls != nil {
				calls = append(calls, fmt.Sprint(*s.ChangeCalls))
			}
			if s.Preserved != nil {
				kept = append(kept, fmt.Sprint(*s.Preserved))
			}
		}
		if len(calls) == 0 {
			continue
		}
		line := fmt.Sprintf("%s @%.12s: model calls for the change [%s]", r.Arm, r.Commit, strings.Join(calls, " "))
		if len(bytes) > 0 {
			line += fmt.Sprintf("; lines changed (added+removed, same measure for every arm) [%s]", strings.Join(bytes, " "))
		}
		if len(synth) > 0 {
			line += fmt.Sprintf("; of which model-written (*_synth.go) [%s]", strings.Join(synth, " "))
		}
		if len(kept) > 0 {
			line += fmt.Sprintf("; pre-change files left byte-identical [%s]", strings.Join(kept, " "))
		}
		out = append(out, line)
	}
	return out
}
