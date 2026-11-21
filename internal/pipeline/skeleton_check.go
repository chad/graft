package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
)

// RenderScratch is how `graft draft --skeleton` checks a proposed skeleton: it
// accepts the spec's drafts into a throwaway provenance repo, imports sk
// through the same path --render uses, renders it with sedum and seeds every
// region's placeholders, all in a temporary directory. It returns the tree's
// root (for the profile's build gate) and a cleanup. The real repo and tree are
// never touched. cfg needs Sedum, Generators, Lang, Module and Tier.
func RenderScratch(ctx context.Context, cfg Config, drafts []drafter.Draft, sk recording.Skeleton) (string, func(), error) {
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
	acc, err := AcceptWith(cfg.Repo, drafts, "graft draft", time.Now(), AcceptOptions{Ext: cfg.lang().Ext})
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
	if _, _, _, _, err := cfg.renderTree(ctx, store, acc); err != nil {
		return fail(err)
	}
	return cfg.Root, cleanup, nil
}
