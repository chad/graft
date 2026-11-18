package pipeline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
)

// sample is one drawn answer after checking.
type sample struct {
	idx     int
	diff    int    // changed lines against the region's current body
	gates   int    // gates passed
	smoke   int    // smoke checks passed, parsed from the eval gate detail; -1 if none printed
	checked bool   // false: refused before any gate ran
	failure string // feedback text when not satisfied
	trial   converge.Trial
}

func (s sample) satisfied() bool { return s.checked && s.trial.Decision.Result.Satisfied }

// attemptSamples runs one attempt with cfg.Samples concurrent model calls for
// the same prompt, checks every answer, and commits only the smallest-diff
// satisfied one (ties: lowest sample index, so the choice is deterministic).
// If none is satisfied, o.LastFailure and *feedback come from the closest
// candidate, which is diagnosis only and is never applied. It reports whether
// a candidate was applied.
func (cfg Config) attemptSamples(ctx context.Context, c converge.Converger, store records.Store, a Accepted,
	file string, regions []converge.Region, prompt string, o *Outcome, feedback *string) (bool, error) {
	n := cfg.Samples
	resps := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resps[i], errs[i] = cfg.Model.Complete(ctx, prompt)
		}(i)
	}
	wg.Wait()
	o.Calls += n
	failed := 0
	var firstErr error
	for _, e := range errs {
		if e != nil {
			failed++
			if firstErr == nil {
				firstErr = e
			}
		}
	}
	if failed == n { // infrastructure, not a verdict on the code
		return false, fmt.Errorf("%w: %v", ErrModel, firstErr)
	}

	base := currentBody(regions, a, file, cfg.Root)
	var all []sample
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			continue // a failed call is not a candidate
		}
		code := model.ExtractCode(resps[i], "go")
		s := sample{idx: i, diff: diffLines(base, code), smoke: -1}
		if cfg.Skeleton != nil && hasPlaceholder(code) {
			s.failure = "the answer still contains the placeholder panic (" + recording.Placeholder + ")"
			all = append(all, s)
			continue
		}
		cand, err := converge.NewCandidate(store, a.ID, file, cfg.Tier, code)
		if err != nil {
			return false, err
		}
		t, err := c.Check(ctx, cand)
		switch {
		case errors.Is(err, converge.ErrMarkerInBody), errors.Is(err, converge.ErrSedumRegion):
			s.failure = err.Error()
		case err != nil:
			return false, fmt.Errorf("%s: %w", a.Entry.Section, err)
		default:
			s.checked, s.trial = true, t
			s.smoke = smokePassed(t.Decision)
			for _, v := range t.Verdicts {
				if v == evidence.Pass {
					s.gates++
				}
			}
			if !t.Decision.Result.Satisfied {
				s.failure = failing(t.Decision)
			}
		}
		all = append(all, s)
	}

	var win *sample
	satisfied := 0
	for i := range all {
		if !all[i].satisfied() {
			continue
		}
		satisfied++
		if win == nil || all[i].diff < win.diff { // strict: ties keep the lower index
			win = &all[i]
		}
	}
	if win != nil {
		d, err := c.Commit(win.trial)
		if err != nil {
			return false, fmt.Errorf("%s: %w", a.Entry.Section, err)
		}
		cfg.say("  sampled %d, %d satisfied, chose #%d (diff %d lines)\n", n, satisfied, win.idx, win.diff)
		cfg.say("  applied (guarantee: %s)\n", d.Guarantee)
		return true, nil
	}

	best := &all[0]
	for i := 1; i < len(all); i++ {
		if closer(all[i], *best) {
			best = &all[i]
		}
	}
	if best.checked {
		o.LastFailure = best.failure
		*feedback = "Your previous answer failed these checks; fix it:\n" + o.LastFailure
	} else {
		o.LastFailure = best.failure
		*feedback = "Your previous answer was refused before checking: " + o.LastFailure
	}
	cfg.say("  sampled %d, 0 satisfied; closest #%d: %s\n", n, best.idx, firstLine(o.LastFailure))
	return false, nil
}

// smokeRe matches the eval gate's "smoke: X/Y checks passed" detail line.
var smokeRe = regexp.MustCompile(`smoke: (\d+)/\d+ checks passed`)

// smokePassed returns the smoke checks passed, or -1 when no evidence prints a count.
func smokePassed(d converge.Decision) int {
	for _, e := range d.Evidence {
		if m := smokeRe.FindStringSubmatch(e.Detail); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return -1
}

// closer reports whether a is a closer miss than b: most gates passed, then
// most smoke checks passed (only when both print a count), then checked over
// refused, then the smallest diff. Ties keep the lower index (a is only ever the later sample).
func closer(a, b sample) bool {
	if a.gates != b.gates {
		return a.gates > b.gates
	}
	if a.smoke >= 0 && b.smoke >= 0 && a.smoke != b.smoke {
		return a.smoke > b.smoke
	}
	if a.checked != b.checked {
		return a.checked
	}
	return a.diff < b.diff
}

// currentBody is the region's present body, or "" if there is none yet.
func currentBody(regions []converge.Region, a Accepted, file, root string) string {
	for _, r := range regions {
		if r.Path == file && r.Owner.Record == a.ID {
			if b, err := converge.RegionBody(root, r); err == nil {
				return b
			}
		}
	}
	return ""
}

// diffLines counts changed lines between two bodies: lines removed from old
// plus lines added in new, over their longest common subsequence.
func diffLines(oldBody, newBody string) int {
	a, b := splitLines(oldBody), splitLines(newBody)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	lcs := prev[len(b)]
	return len(a) + len(b) - 2*lcs
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
