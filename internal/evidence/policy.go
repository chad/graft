package evidence

// Policy maps a risk tier to the evidence kinds it requires (SPEC.md §6–7).
// A tier absent from the policy requires nothing.
type Policy map[string][]Kind

// Result says whether a region's fresh evidence satisfies its tier.
type Result struct {
	Satisfied bool
	Missing   []Kind // required kinds with no fresh passing evidence
	Failing   []Kind // required kinds whose latest fresh evidence failed
}

// Evaluate checks the fresh evidence for cur against tier. For each required
// kind, the LATEST fresh entry of that kind decides: a pass satisfies it, a
// fail or inconclusive does not. Stale evidence never counts.
func (p Policy) Evaluate(l *Log, tier string, cur Pin) Result {
	latest := map[Kind]Evidence{}
	for _, e := range l.Fresh(cur) {
		latest[e.Kind] = e // Fresh is oldest-first, so the last write wins
	}
	r := Result{Satisfied: true}
	for _, k := range p[tier] {
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
	return r
}
