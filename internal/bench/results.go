package bench

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Schema is the results format version. v2: records carry an ID, and layer
// attribution moved out of samples into separate append-only records.
const Schema = 2

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

// Layer is where a failure is attributed (SPEC §10).
type Layer string

const (
	LayerSelection   Layer = "selection"   // the model chose wrong actions/kwargs
	LayerRender      Layer = "render"      // deterministic rendering was wrong
	LayerConvergence Layer = "convergence" // synthesis passed its gate but was wrong, or never converged
	LayerUnknown     Layer = "unknown"     // attributed, and the answer was "cannot tell"
)

func validLayer(l Layer) bool {
	switch l {
	case LayerSelection, LayerRender, LayerConvergence, LayerUnknown:
		return true
	}
	return false
}

func validLayers(ls []Layer) error {
	if len(ls) == 0 {
		return errors.New("bench: an attribution names at least one layer (LayerUnknown if it cannot be told)")
	}
	seen := map[Layer]bool{}
	for _, l := range ls {
		if !validLayer(l) {
			return fmt.Errorf("bench: unknown layer %q", l)
		}
		if seen[l] {
			return fmt.Errorf("bench: layer %q named twice", l)
		}
		seen[l] = true
	}
	if seen[LayerUnknown] && len(ls) > 1 {
		return errors.New("bench: LayerUnknown cannot be combined with a named layer")
	}
	return nil
}

// Sample is one draw. It carries no attribution: that is added later, by
// separate Attribution records, so the run log is never rewritten.
type Sample struct {
	Outcome Outcome `json:"outcome"`
	Checks  int     `json:"checks"`
	Passed  int     `json:"passed"`
	Reason  string  `json:"reason,omitempty"`
}

// Attribution says which layer(s) one failed sample's failure came from.
// Attributions are append-only; for a (record, sample) the latest one wins.
type Attribution struct {
	Schema    int       `json:"schema"`
	RecordID  string    `json:"record_id"`
	SampleIdx int       `json:"sample_idx"`
	Layers    []Layer   `json:"layers"` // one or more; LayerUnknown alone when it cannot be told
	Reason    string    `json:"reason"`
	Evidence  string    `json:"evidence,omitempty"` // e.g. a recording, a diff, an evidence-log id
	By        string    `json:"by"`
	At        time.Time `json:"at"`
}

// Record is one run of one arm on one case. Records are appended, never edited.
type Record struct {
	Schema     int        `json:"schema"`
	ID         string     `json:"id"` // random 128-bit id, assigned by Append
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
// Records from another schema (there are no v1 logs in the wild: v1 never
// merged) are shown and counted in nothing.
// Smoke, dirty, unstated and short runs stay in the log and are shown, but
// are counted in nothing.
func (r Record) Citable() (bool, string) {
	switch {
	case r.Schema != Schema:
		return false, fmt.Sprintf("schema v%d, this harness reads v%d", r.Schema, Schema)
	case r.ID == "":
		return false, "no record id: it was not written by Store.Append"
	case r.Resolution == Smoke:
		return false, "smoke run: proves plumbing, measures nothing"
	case r.Resolution == Unstated || r.Resolution == "":
		return false, "no sample size was declared"
	case Samples[r.Resolution] == 0:
		return false, fmt.Sprintf("unknown resolution %q", r.Resolution)
	case r.Dirty:
		return false, "dirty tree: the commit pins nothing"
	case len(r.Samples) < Samples[r.Resolution]:
		return false, fmt.Sprintf("%d samples, below the %d its resolution declares", len(r.Samples), Samples[r.Resolution])
	}
	for _, s := range r.Samples {
		if s.Outcome != Unreachable {
			return true, ""
		}
	}
	return false, "every sample was unreachable: the harness failed, nothing was measured"
}

// Store is an append-only directory: <case>.jsonl holds run records and
// <case>.attrib.jsonl holds attributions.
type Store struct{ Dir string }

func (s Store) path(caseName, suffix string) (string, error) {
	if caseName == "" || filepath.Base(caseName) != caseName || caseName == "." || caseName == ".." {
		return "", fmt.Errorf("bench: bad case name %q", caseName)
	}
	return filepath.Join(s.Dir, caseName+suffix), nil
}

// Append adds r to its case's log and returns it with Schema, At and ID set.
// It refuses unknown arms, resolutions and outcomes and impossible check counts.
//
// Append is not idempotent: each call is a new run with a new random id. If
// it returns an error after the bytes reached the file (a failed Sync), a
// retry records the run twice and N doubles. Inspect the log before retrying.
func (s Store) Append(r Record) (Record, error) {
	p, err := s.path(r.Case, ".jsonl")
	if err != nil {
		return r, err
	}
	switch r.Arm {
	case ArmGraft, ArmBaseline, ArmIntent:
	default:
		return r, fmt.Errorf("bench: unknown arm %q", r.Arm)
	}
	switch r.Resolution {
	case Smoke, Coarse, Fine, Unstated:
	default:
		return r, fmt.Errorf("bench: unknown resolution %q", r.Resolution)
	}
	for i, smp := range r.Samples {
		switch smp.Outcome {
		case Works, Disagreed, Broke, Unreachable:
		default:
			return r, fmt.Errorf("bench: sample %d: unknown outcome %q", i, smp.Outcome)
		}
		if smp.Passed < 0 || smp.Passed > smp.Checks {
			return r, fmt.Errorf("bench: sample %d: passed %d of %d checks", i, smp.Passed, smp.Checks)
		}
	}
	r.Schema = Schema
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	// A random id, not a content hash: two identical runs are two runs.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return r, err
	}
	r.ID = hex.EncodeToString(nonce[:])
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	return r, appendLine(s.Dir, p, b)
}

// Attribute appends an attribution for sample idx of a stored record.
func (s Store) Attribute(caseName string, a Attribution) error {
	p, err := s.path(caseName, ".attrib.jsonl")
	if err != nil {
		return err
	}
	if a.RecordID == "" {
		return errors.New("bench: an attribution names a record id")
	}
	if err := validLayers(a.Layers); err != nil {
		return err
	}
	recs, _, err := s.Load(caseName)
	if err != nil {
		return err
	}
	var target *Record
	for i := range recs {
		if recs[i].ID == a.RecordID && recs[i].Schema == Schema {
			if target != nil {
				return fmt.Errorf("bench: record id %q is not unique in case %s", a.RecordID, caseName)
			}
			target = &recs[i]
		}
	}
	switch {
	case target == nil:
		return fmt.Errorf("bench: no record %q in case %s", a.RecordID, caseName)
	case a.SampleIdx < 0 || a.SampleIdx >= len(target.Samples):
		return fmt.Errorf("bench: record %s has no sample %d", a.RecordID, a.SampleIdx)
	case target.Samples[a.SampleIdx].Outcome == Works || target.Samples[a.SampleIdx].Outcome == Unreachable:
		return fmt.Errorf("bench: sample %d did not fail (%s); only failures are attributed", a.SampleIdx, target.Samples[a.SampleIdx].Outcome)
	}
	a.Schema = Schema
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return appendLine(s.Dir, p, b)
}

// appendLine appends one JSON line durably. If the file ends in a torn line
// (a crash mid-write), a newline is written first so the torn bytes stay on
// their own line, which Load reports as corrupt, instead of corrupting this one.
func appendLine(dir, p string, b []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// Serialise writers, so the torn-line check and the write are one step and
	// log order (which decides "latest attribution wins") is well defined.
	if err := lockFile(f); err != nil {
		f.Close()
		return err
	}
	defer unlockFile(f)
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			b = append([]byte{'\n'}, b...)
		}
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

// Corrupt is a line Load could not read. It is reported, never dropped silently.
type Corrupt struct {
	File string
	Line int
	Err  string
}

func loadLines[T any](p string) ([]T, []Corrupt, error) {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var out []T
	var bad []Corrupt
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			bad = append(bad, Corrupt{File: filepath.Base(p), Line: n, Err: err.Error()})
			continue
		}
		out = append(out, v)
	}
	return out, bad, sc.Err()
}

// Load reads every record for a case, oldest first, and any corrupt lines.
func (s Store) Load(caseName string) ([]Record, []Corrupt, error) {
	p, err := s.path(caseName, ".jsonl")
	if err != nil {
		return nil, nil, err
	}
	return loadLines[Record](p)
}

// LoadAttributions reads every attribution for a case, oldest first.
func (s Store) LoadAttributions(caseName string) ([]Attribution, []Corrupt, error) {
	p, err := s.path(caseName, ".attrib.jsonl")
	if err != nil {
		return nil, nil, err
	}
	return loadLines[Attribution](p)
}

// Partition is one (fixture, commit, model, arm) cell. Records are never
// pooled across fixtures (different questions), commits (different
// answerers) or models (different capabilities).
type Partition struct {
	Fixture, Commit, Model string
	Arm                    Arm
	Works                  Interval      // samples that worked / eligible samples (unreachable excluded)
	Checks                 Interval      // checks passed / checks attempted
	ByLayer                map[Layer]int // a failure attributed to two layers counts in both
	Unattributed           int           // failures with no attribution yet
	Outcomes               map[Outcome]int
	Records                int
}

// Aggregate pools the CITABLE records of a case into partitions, sorted by
// fixture, commit, model, arm. Non-citable records are returned separately,
// with their reasons, so a report shows them and counts them in nothing.
// For each failed sample the latest attribution decides its layers.
func Aggregate(recs []Record, attrs []Attribution) (parts []Partition, excluded map[int]string, err error) {
	type key struct {
		fixture, commit, model string
		arm                    Arm
	}
	type acc struct {
		works, eligible, passed, checks, records, unattributed int
		byLayer                                                map[Layer]int
		outcomes                                               map[Outcome]int
	}
	type sampleKey struct {
		id  string
		idx int
	}
	// Attributions loaded from disk are validated again: a hand-edited or
	// foreign line cannot invent a layer. An invalid one is ignored, so the
	// sample falls back to the previous valid attribution or to unattributed.
	latest := map[sampleKey][]Layer{}
	for _, a := range attrs {
		if a.Schema != Schema || a.RecordID == "" || validLayers(a.Layers) != nil {
			continue
		}
		latest[sampleKey{a.RecordID, a.SampleIdx}] = a.Layers
	}
	// Duplicate record ids are excluded outright: an attribution could not say which it meant.
	idCount := map[string]int{}
	for _, r := range recs {
		idCount[r.ID]++
	}
	cells := map[key]*acc{}
	excluded = map[int]string{}
	for i, r := range recs {
		if ok, why := r.Citable(); !ok {
			excluded[i] = why
			continue
		}
		if idCount[r.ID] > 1 {
			excluded[i] = fmt.Sprintf("record id %.12s is not unique in this case", r.ID)
			continue
		}
		k := key{r.Fixture, r.Commit, r.Model, r.Arm}
		a := cells[k]
		if a == nil {
			a = &acc{byLayer: map[Layer]int{}, outcomes: map[Outcome]int{}}
			cells[k] = a
		}
		a.records++
		for i, s := range r.Samples {
			a.outcomes[s.Outcome]++
			if s.Outcome == Unreachable {
				continue
			}
			a.eligible++
			a.checks += s.Checks
			a.passed += s.Passed
			if s.Outcome == Works {
				a.works++
				continue
			}
			ls, ok := latest[sampleKey{r.ID, i}]
			if !ok {
				a.unattributed++
			}
			for _, l := range ls {
				a.byLayer[l]++
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
		parts = append(parts, Partition{Fixture: k.fixture, Commit: k.commit, Model: k.model, Arm: k.arm, Works: w, Checks: c,
			ByLayer: a.byLayer, Unattributed: a.unattributed, Outcomes: a.outcomes, Records: a.records})
	}
	sort.Slice(parts, func(i, j int) bool {
		a, b := parts[i], parts[j]
		if a.Fixture != b.Fixture {
			return a.Fixture < b.Fixture
		}
		if a.Commit != b.Commit {
			return a.Commit < b.Commit
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Arm < b.Arm
	})
	return parts, excluded, nil
}
