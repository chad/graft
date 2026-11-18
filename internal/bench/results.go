package bench

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Schema is the results format version.
const Schema = 1

// Arm is what produced the code under test.
type Arm string

const (
	ArmGraft    Arm = "graft"    // the full pipeline
	ArmBaseline Arm = "baseline" // same intent, same model, one call, no pipeline
	ArmIntent   Arm = "intent"   // one sentence and the runtime contract
)

// Resolution is the question a run's sample size answers.
type Resolution string

const (
	Smoke    Resolution = "smoke"    // does the plumbing work at all?
	Coarse   Resolution = "coarse"   // differences that are enormous
	Fine     Resolution = "fine"     // moving a rate that is already high
	Unstated Resolution = "unstated" // no question was declared
)

// Samples is how many samples a resolution calls for.
var Samples = map[Resolution]int{Smoke: 2, Coarse: 5, Fine: 30}

// Outcome is what one sample did. Unreachable is infrastructure, not a measurement.
type Outcome string

const (
	Works       Outcome = "works"       // booted and every check passed
	Disagreed   Outcome = "disagreed"   // booted and answered wrongly
	Broke       Outcome = "broke"       // never booted
	Unreachable Outcome = "unreachable" // the harness, not the code, failed
)

// Layer is where a failure is attributed (SPEC §10). Attribution is filled in
// by the attribution step; the harness only stores and counts it.
type Layer string

const (
	LayerNone        Layer = ""
	LayerSelection   Layer = "selection"   // the model chose wrong actions/kwargs
	LayerRender      Layer = "render"      // deterministic rendering was wrong
	LayerConvergence Layer = "convergence" // synthesis passed its gate but was wrong, or never converged
)

// Sample is one draw.
type Sample struct {
	Outcome Outcome `json:"outcome"`
	Layer   Layer   `json:"layer,omitempty"`
	Checks  int     `json:"checks"`
	Passed  int     `json:"passed"`
	Reason  string  `json:"reason,omitempty"`
}

// Record is one run of one arm on one case. Records are appended, never edited.
type Record struct {
	Schema     int        `json:"schema"`
	Case       string     `json:"case"`
	Arm        Arm        `json:"arm"`
	Commit     string     `json:"commit"`  // graft's commit: the answerer
	Dirty      bool       `json:"dirty"`   // uncommitted changes: the commit pins nothing
	Fixture    string     `json:"fixture"` // digest of spec + checks + runtime contract: the question
	Model      string     `json:"model"`
	Resolution Resolution `json:"resolution"`
	Samples    []Sample   `json:"samples"`
	At         time.Time  `json:"at"`
}

// Citable reports whether a record may enter any aggregate, and if not, why.
// Smoke, dirty, unstated and short runs stay in the log and are shown, but
// are counted in nothing.
func (r Record) Citable() (bool, string) {
	switch {
	case r.Resolution == Smoke:
		return false, "smoke run: proves plumbing, measures nothing"
	case r.Resolution == Unstated || r.Resolution == "":
		return false, "no sample size was declared"
	case r.Dirty:
		return false, "dirty tree: the commit pins nothing"
	case len(r.Samples) < Samples[r.Resolution]:
		return false, fmt.Sprintf("%d samples, below the %d its resolution declares", len(r.Samples), Samples[r.Resolution])
	}
	return true, ""
}

// Store is an append-only directory of <case>.jsonl files.
type Store struct{ Dir string }

// Append adds r to its case's log. It refuses unknown arms and outcomes.
func (s Store) Append(r Record) error {
	if r.Case == "" || filepath.Base(r.Case) != r.Case {
		return fmt.Errorf("bench: bad case name %q", r.Case)
	}
	switch r.Arm {
	case ArmGraft, ArmBaseline, ArmIntent:
	default:
		return fmt.Errorf("bench: unknown arm %q", r.Arm)
	}
	for i, smp := range r.Samples {
		switch smp.Outcome {
		case Works, Disagreed, Broke, Unreachable:
		default:
			return fmt.Errorf("bench: sample %d: unknown outcome %q", i, smp.Outcome)
		}
		if smp.Passed < 0 || smp.Passed > smp.Checks {
			return fmt.Errorf("bench: sample %d: passed %d of %d checks", i, smp.Passed, smp.Checks)
		}
	}
	r.Schema = Schema
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, r.Case+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load reads every record for a case, oldest first.
func (s Store) Load(caseName string) ([]Record, error) {
	f, err := os.Open(filepath.Join(s.Dir, caseName+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("bench: %s line %d: %w", caseName, n, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Partition is one (fixture, commit, arm) cell. Records are never pooled
// across fixtures (different questions) or commits (different answerers).
type Partition struct {
	Fixture, Commit string
	Arm             Arm
	Works           Interval // samples that worked / eligible samples (unreachable excluded)
	Checks          Interval // checks passed / checks attempted
	ByLayer         map[Layer]int
	Outcomes        map[Outcome]int
	Records         int
}

// Aggregate pools the CITABLE records of a case into partitions, sorted by
// fixture, commit, arm. Non-citable records are returned separately, with
// their reasons, so a report shows them and counts them in nothing.
func Aggregate(recs []Record) (parts []Partition, excluded map[int]string, err error) {
	type key struct {
		fixture, commit string
		arm             Arm
	}
	type acc struct {
		works, eligible, passed, checks, records int
		byLayer                                  map[Layer]int
		outcomes                                 map[Outcome]int
	}
	cells := map[key]*acc{}
	excluded = map[int]string{}
	for i, r := range recs {
		if ok, why := r.Citable(); !ok {
			excluded[i] = why
			continue
		}
		k := key{r.Fixture, r.Commit, r.Arm}
		a := cells[k]
		if a == nil {
			a = &acc{byLayer: map[Layer]int{}, outcomes: map[Outcome]int{}}
			cells[k] = a
		}
		a.records++
		for _, s := range r.Samples {
			a.outcomes[s.Outcome]++
			if s.Outcome == Unreachable {
				continue
			}
			a.eligible++
			a.checks += s.Checks
			a.passed += s.Passed
			if s.Outcome == Works {
				a.works++
			} else {
				a.byLayer[s.Layer]++
			}
		}
	}
	for k, a := range cells {
		w, err := Wilson(a.works, a.eligible)
		if err != nil {
			return nil, nil, err
		}
		c, err := Wilson(a.passed, a.checks)
		if err != nil {
			return nil, nil, err
		}
		parts = append(parts, Partition{Fixture: k.fixture, Commit: k.commit, Arm: k.arm, Works: w, Checks: c,
			ByLayer: a.byLayer, Outcomes: a.outcomes, Records: a.records})
	}
	sort.Slice(parts, func(i, j int) bool {
		a, b := parts[i], parts[j]
		if a.Fixture != b.Fixture {
			return a.Fixture < b.Fixture
		}
		if a.Commit != b.Commit {
			return a.Commit < b.Commit
		}
		return a.Arm < b.Arm
	})
	return parts, excluded, nil
}
