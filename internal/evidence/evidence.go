// Package evidence is graft's single evidence format (SPEC.md §7).
//
// Evidence pins what it read: the record's gen_hash, the hash of the region it
// speaks for, and the hash of the specs it ran (if any). It is stale the moment
// any of those moves. The record version is recorded but not compared: a
// version bump that changes none of them (a status flip, a reworded reason)
// is no reason to re-verify. (SPEC §7; decided in PR #1 review.)
//
// Evidence lives in an append-only, hash-chained log with a sidecar head
// pinning its committed prefix. Verify detects ACCIDENTAL damage (an edited,
// reordered or lost committed entry, a torn write); it does not detect a
// deliberate rewrite by someone with write access. See SPEC §7.
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

// FormatVersion is the evidence encoding version, covered by every ID.
const FormatVersion = 1

// Pin is what the evidence was produced against. Its field names mirror
// records.Pin; it is kept local so this package depends on no other layer.
type Pin struct {
	RecordID   string `json:"record_id"`
	Version    int    `json:"version"`             // recorded, not compared
	GenHash    string `json:"gen_hash"`            // what drives generation
	RegionHash string `json:"region_hash"`         // the bytes the evidence speaks for
	SpecHash   string `json:"spec_hash,omitempty"` // the specs it ran; "" if none
}

// Evidence is one entry in the log.
type Evidence struct {
	V        int       `json:"v"`    // FormatVersion
	ID       string    `json:"id"`   // hash of this entry, including Prev
	Prev     string    `json:"prev"` // ID of the previous entry; "" for the first
	Kind     Kind      `json:"kind"`
	Verdict  Verdict   `json:"verdict"`
	Pin      Pin       `json:"pin"`
	By       string    `json:"by"`               // who or what produced it (a DID, a tool name)
	Detail   string    `json:"detail,omitempty"` // human-readable summary
	Artifact string    `json:"artifact,omitempty"`
	At       time.Time `json:"at"`
}

// Moved names which compared parts of the pin differ from cur: any of
// "gen_hash", "region_hash", "spec_hash". Empty means fresh. A different
// RecordID is reported as "record_id": the evidence is not about cur at all.
func (e Evidence) Moved(cur Pin) []string {
	if e.Pin.RecordID != cur.RecordID {
		return []string{"record_id"}
	}
	var m []string
	if e.Pin.GenHash != cur.GenHash {
		m = append(m, "gen_hash")
	}
	if e.Pin.RegionHash != cur.RegionHash {
		m = append(m, "region_hash")
	}
	if e.Pin.SpecHash != cur.SpecHash {
		m = append(m, "spec_hash")
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
