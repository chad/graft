// Package evidence is graft's single evidence format (SPEC.md §7).
//
// Every piece of evidence pins three things — the record version, its
// gen_hash, and the hash of the region it speaks for — and is stale the moment
// any of the three moves. Evidence lives in an append-only, hash-chained log,
// so history cannot be rewritten without Verify noticing.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Kind is what produced the evidence.
type Kind string

const (
	TestRun        Kind = "test_run"
	Eval           Kind = "eval"
	HumanSignoff   Kind = "human_signoff"
	StaticAnalysis Kind = "static_analysis"
	Conformance    Kind = "conformance"
)

// Verdict is the outcome the evidence records.
type Verdict string

const (
	Pass         Verdict = "pass"
	Fail         Verdict = "fail"
	Inconclusive Verdict = "inconclusive"
)

// Pin is the three-hash pin: what the evidence was produced against.
// It mirrors records.Pin plus the region hash, kept local so this package has
// no dependency on the record or render layers.
type Pin struct {
	RecordID      string `json:"record_id"`
	RecordVersion int    `json:"record_version"`
	GenHash       string `json:"gen_hash"`
	RegionHash    string `json:"region_hash"`
}

// Evidence is one entry in the log.
type Evidence struct {
	ID       string    `json:"id"`   // hash of this entry, including Prev
	Prev     string    `json:"prev"` // ID of the previous entry; "" for the first
	Kind     Kind      `json:"kind"`
	Verdict  Verdict   `json:"verdict"`
	Pin      Pin       `json:"pin"`
	RiskTier string    `json:"risk_tier"`
	By       string    `json:"by"`               // who or what produced it (a DID, a tool name)
	Detail   string    `json:"detail,omitempty"` // human-readable summary
	Artifact string    `json:"artifact,omitempty"`
	At       time.Time `json:"at"`
}

// Moved names which parts of the pin differ from cur: any of
// "record_version", "gen_hash", "region_hash". Empty means fresh.
// A different RecordID is reported as "record_id": the evidence is not about cur at all.
func (e Evidence) Moved(cur Pin) []string {
	var m []string
	if e.Pin.RecordID != cur.RecordID {
		return []string{"record_id"}
	}
	if e.Pin.RecordVersion != cur.RecordVersion {
		m = append(m, "record_version")
	}
	if e.Pin.GenHash != cur.GenHash {
		m = append(m, "gen_hash")
	}
	if e.Pin.RegionHash != cur.RegionHash {
		m = append(m, "region_hash")
	}
	return m
}

// Stale reports whether the evidence no longer speaks for cur.
func (e Evidence) Stale(cur Pin) bool { return len(e.Moved(cur)) > 0 }

// computeID hashes the entry with ID cleared. encoding/json emits struct
// fields in declaration order, so the encoding is canonical for this type.
func computeID(e Evidence) (string, error) {
	e.ID = ""
	e.At = e.At.UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("evidence: encode: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
