package converge

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chad/graft/internal/records"
	"github.com/chad/graft/internal/render"
)

// ErrSedumRegion: the synthesis would overlap or alter a sedum-owned region.
// Rendering is guaranteed; convergence is merely checked, and the weaker layer
// never writes into the stronger one's regions (SPEC §2, §5).
var ErrSedumRegion = errors.New("converge: synthesis would touch a sedum-owned region")

// NewCandidate builds a candidate for record id from the records layer: the
// version and gen_hash are the record's current ones, never caller-supplied.
func NewCandidate(s records.Store, id, path, tier, specHash, body string) (Candidate, error) {
	p, err := s.Current(id)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Path: path, RecordID: p.RecordID, Version: p.Version, GenHash: p.GenHash,
		SpecHash: specHash, Tier: tier, Body: body}, nil
}

// guardSedum refuses when the record's synthesis region overlaps a sedum
// region in before, or when next changes any sedum region at all (count,
// order, owner, marker hash, or body).
func guardSedum(cand Candidate, before, next []byte) error {
	was, err := render.ParseFile(cand.Path, before)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSedumRegion, err)
	}
	synth, err := parse(cand.Path, before)
	if err != nil {
		return err
	}
	for _, r := range synth {
		if r.Owner.Record != cand.RecordID {
			continue
		}
		b, e := r.begin+1, r.end+1
		for _, s := range was {
			if b <= s.End && s.Start <= e {
				return fmt.Errorf("%w: %s lines %d-%d overlap sedum region %d-%d (record %s)",
					ErrSedumRegion, cand.Path, b, e, s.Start, s.End, s.Owner.RecordID)
			}
		}
	}
	now, err := render.ParseFile(cand.Path, next)
	if err != nil {
		return fmt.Errorf("%w: the rendered file no longer parses as sedum: %v", ErrSedumRegion, err)
	}
	if len(now) != len(was) {
		return fmt.Errorf("%w: %d sedum regions before, %d after", ErrSedumRegion, len(was), len(now))
	}
	bl, nl := splitLines(before), splitLines(next)
	for i := range was {
		a, b := was[i], now[i]
		// Byte-identical, marker lines included (kwargs, ids, comment suffixes, CRLF).
		if strings.Join(bl[a.Start-1:a.End], "") != strings.Join(nl[b.Start-1:b.End], "") {
			return fmt.Errorf("%w: sedum region for %s at line %d would change", ErrSedumRegion, a.Owner.RecordID, a.Start)
		}
	}
	return nil
}
