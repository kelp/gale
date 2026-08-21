package main

import (
	"context"
	"fmt"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
)

// lockFetch is the unused Phase 1 lock-only request. It is not
// the live gale lock command.
type lockFetch struct {
	Source index.Source
	Roots  []string
}

// runLockFetch pins one index_commit, resolves every declared
// root against that session, and writes a v2 lock. It does not
// fetch artifacts, register, swap current, or write gale.toml.
func runLockFetch(ctx context.Context, c *cmdContext, req lockFetch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	draft := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: append([]string(nil), req.Roots...)},
		},
		Packages: make(map[string]lockfile.V2Package, len(req.Roots)),
	}
	if _, err := pkgsFromV2Lock(draft); err != nil {
		return err
	}
	return filelock.With(mutateLockPath(c.GaleDir), func() error {
		return writeLockFetch(ctx, c, req, draft)
	})
}

func writeLockFetch(
	ctx context.Context, c *cmdContext, req lockFetch, draft *lockfile.V2,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sess, err := index.Open(ctx, req.Source)
	if err != nil {
		return fmt.Errorf("opening index: %w", err)
	}
	for _, root := range req.Roots {
		name, version, err := lockfile.SplitV2Root(root)
		if err != nil {
			return fmt.Errorf("lock root: %w", err)
		}
		got, ver, err := sess.Resolve(ctx, name, version)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", root, err)
		}
		draft.Packages[name+"@"+got] = lockfile.V2Package{
			Artifacts: v2ArtifactsFromIndex(ver.Artifacts, sess.Commit),
		}
	}
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	if err := lockfile.WriteV2(lp, draft); err != nil {
		return fmt.Errorf("writing lock: %w", err)
	}
	return nil
}

func v2ArtifactsFromIndex(
	arts map[string]index.Artifact, commit string,
) map[string]lockfile.V2Artifact {
	out := make(map[string]lockfile.V2Artifact, len(arts))
	for plat, a := range arts {
		out[plat] = v2ArtifactFromIndex(a, commit)
	}
	return out
}

func v2ArtifactFromIndex(a index.Artifact, commit string) lockfile.V2Artifact {
	files := make([]lockfile.V2File, 0, len(a.Files))
	for _, f := range a.Files {
		files = append(files, lockfile.V2File{
			Src: f.Src, Dest: f.Dest, Mode: f.Mode,
		})
	}
	art := lockfile.V2Artifact{
		URL:         a.URL,
		Format:      a.Format,
		SHA256:      a.SHA256,
		TreeDigest:  a.TreeDigest,
		Method:      provenance.MethodFetch,
		Strip:       a.Strip,
		HashSource:  a.HashSource,
		IndexCommit: commit,
		Files:       files,
	}
	if a.Attestation != nil {
		art.Attestation = &lockfile.V2Attestation{}
	}
	return art
}
