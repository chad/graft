// Package records holds graft's record-layer types.
//
// This file is the minimal Pin struct M2 (internal/render) needs. M1 owns the
// rest of the package and will reconcile with it.
package records

// Pin identifies a record at a version, with the hash over its
// generation-driving fields.
type Pin struct {
	RecordID string // stable id, never derived from content
	Version  int
	GenHash  string // hash over generation-driving fields only
}
