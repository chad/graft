package bench

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Report writes a case's results: every citable partition with intervals,
// the only comparisons the harness permits (graft vs each control, within one
// fixture and commit), and every excluded record with its reason.
func Report(w io.Writer, caseName string, recs []Record) error {
	parts, excluded, err := Aggregate(recs)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "## %s\n\n", caseName)
	if len(parts) == 0 {
		fmt.Fprintln(w, "No citable runs.")
	} else {
		fmt.Fprintln(w, "| fixture | commit | arm | works | checks passed | failures by layer |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|")
		for _, p := range parts {
			fmt.Fprintf(w, "| %.12s | %.12s | %s | %s | %s | %s |\n", p.Fixture, p.Commit, p.Arm, p.Works, p.Checks, layers(p.ByLayer))
		}
		fmt.Fprintln(w)
		for _, cmp := range comparisons(parts) {
			fmt.Fprintln(w, "- "+cmp)
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

func layers(m map[Layer]int) string {
	if len(m) == 0 {
		return "—"
	}
	var ks []string
	for l, n := range m {
		name := string(l)
		if name == "" {
			name = "unattributed"
		}
		ks = append(ks, fmt.Sprintf("%s %d", name, n))
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// comparisons pairs graft with each control inside the same fixture and commit.
func comparisons(parts []Partition) []string {
	type cell struct{ fixture, commit string }
	by := map[cell]map[Arm]Partition{}
	var order []cell
	for _, p := range parts {
		c := cell{p.Fixture, p.Commit}
		if by[c] == nil {
			by[c] = map[Arm]Partition{}
			order = append(order, c)
		}
		by[c][p.Arm] = p
	}
	var out []string
	for _, c := range order {
		g, ok := by[c][ArmGraft]
		if !ok {
			continue
		}
		for _, ctl := range []Arm{ArmBaseline, ArmIntent} {
			if p, ok := by[c][ctl]; ok {
				out = append(out, Compare("graft", g.Works, string(ctl), p.Works))
			}
		}
	}
	return out
}
