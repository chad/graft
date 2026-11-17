package evidence

import (
	"errors"
	"fmt"
)

// ErrUnknownTier: the tier is not in the policy. A misspelled tier must not
// pass by requiring nothing.
var ErrUnknownTier = errors.New("evidence: unknown risk tier")

// Policy maps a risk tier to the evidence kinds it requires (SPEC.md §6–7).
// A tier that genuinely requires nothing must be listed with an empty slice.
type Policy map[string][]Kind

// Result says whether a region's fresh evidence satisfies its tier.
type Result struct {
	Satisfied bool
	Missing   []Kind // required kinds with no fresh evidence
	Failing   []Kind // required kinds whose latest fresh evidence did not pass
}

// Evaluate verifies the log, then checks the fresh evidence for cur against
// tier. For each required kind the LATEST fresh entry in log order decides
// (Append guarantees log order is also time order): a pass satisfies it, a
// fail or inconclusive does not. Stale evidence never counts.
func (p Policy) Evaluate(l *Log, tier string, cur Pin) (Result, error) {
	required, ok := p[tier]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownTier, tier)
	}
	// Verify re-reads log and head from disk under a shared lock, so a handle
	// opened before someone else appended still evaluates the current log.
	if err := l.Verify(); err != nil {
		return Result{}, err
	}
	latest := map[Kind]Evidence{}
	for _, e := range l.Fresh(cur) {
		latest[e.Kind] = e
	}
	r := Result{Satisfied: true}
	for _, k := range required {
		e, ok := latest[k]
		switch {
		case !ok:
			r.Missing = append(r.Missing, k)
			r.Satisfied = false
		case e.Verdict != Pass:
			r.Failing = append(r.Failing, k)
			r.Satisfied = false
		}
	}
	return r, nil
}
