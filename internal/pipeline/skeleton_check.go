package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
	"github.com/livecodelife/provenance/record"
)

// RenderScratch is how `graft draft --skeleton` checks a proposed skeleton: it
// accepts the spec's drafts into a throwaway provenance repo, imports sk
// through the same path --render uses, renders it with sedum and seeds every
// region's placeholders, all in a temporary directory. It returns the tree's
// root (for the profile's build gate) and a cleanup. The real repo and tree are
// never touched. files places each section's scratch record (SectionFiles), so
// the skeleton is checked at the paths it will really be imported at. cfg needs Sedum, Generators, Lang, Module and Tier.
func RenderScratch(ctx context.Context, cfg Config, drafts []drafter.Draft, files map[string]string, sk recording.Skeleton) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "graft-draft-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	fail := func(err error) (string, func(), error) { cleanup(); return "", nil, err }
	cfg.Repo, cfg.Root = filepath.Join(tmp, "repo"), filepath.Join(tmp, "app")
	cfg.Skeleton, cfg.Progress = &sk, nil
	if cfg.Tier == "" {
		cfg.Tier = "low"
	}
	if err := os.MkdirAll(cfg.Repo, 0o755); err != nil {
		return fail(err)
	}
	acc, err := AcceptWith(cfg.Repo, drafts, "graft draft", time.Now(), AcceptOptions{Ext: cfg.lang().Ext, Files: files})
	if err != nil {
		return fail(fmt.Errorf("scratch accept: %w", err))
	}
	if err := bootstrap(cfg); err != nil {
		return fail(err)
	}
	store, err := records.OpenDir(cfg.Repo)
	if err != nil {
		return fail(err)
	}
	if _, _, _, err := cfg.renderTree(ctx, store, acc); err != nil {
		return fail(err)
	}
	return cfg.Root, cleanup, nil
}

// SectionFiles is the file each section's drafted shape must be placed at. A
// section that already has a record owns that record's affected_scope, read
// from the record and never recomputed (since #36 it is the source of truth),
// and a scope that is not exactly one file is refused, the rule #36 enforces.
// A section with no record yet gets the derived SectionFile path. repo may be
// empty: every section is then new.
func SectionFiles(repo string, drafts []drafter.Draft, ext string) (map[string]string, error) {
	out := map[string]string{}
	for _, d := range drafts {
		out[d.Section] = SectionFile(d.Section, ext)
	}
	if repo == "" {
		return out, nil
	}
	idx, err := LoadIndex(repo)
	if err != nil {
		return nil, err
	}
	var accepted []drafter.Accepted
	for id, e := range idx.Records {
		accepted = append(accepted, drafter.Accepted{ID: id, DerivedFrom: e.DerivedFrom, Anchor: e.Anchor})
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ID < accepted[j].ID })
	fs := &record.FilesStore{Root: repo}
	var bad []string
	for _, p := range drafter.Map(drafts, accepted) {
		if p.Kind != drafter.Revise {
			continue
		}
		r, _, err := fs.Load(p.RecordID)
		if err != nil {
			return nil, fmt.Errorf("pipeline: load %s: %w", p.RecordID, err)
		}
		if len(r.AffectedScope) != 1 {
			bad = append(bad, fmt.Sprintf("%s: record %s has affected_scope %v, not exactly one file", p.Draft.Section, p.RecordID, r.AffectedScope))
			continue
		}
		out[p.Draft.Section] = r.AffectedScope[0]
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("pipeline: cannot place drafted shapes:\n  %s", strings.Join(bad, "\n  "))
	}
	return out, nil
}
