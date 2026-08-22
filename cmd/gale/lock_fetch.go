package main

import (
	"context"
	"fmt"

	"github.com/kelp/gale/internal/attestation"
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
		arts, err := v2ArtifactsFromIndex(name, ver.Artifacts, sess.Commit)
		if err != nil {
			return err
		}
		draft.Packages[name+"@"+got] = lockfile.V2Package{Artifacts: arts}
	}
	return writeLockDoc(c, draft)
}

// writeLockDoc writes an already-resolved v2 document under the
// caller's mutation lock. Resolution belongs to the caller; this
// never opens the index again.
func writeLockDoc(c *cmdContext, draft *lockfile.V2) error {
	lp, err := lockfilePath(c.GalePath)
	if err != nil {
		return err
	}
	if err := lockfile.WriteV2(lp, draft); err != nil {
		return fmt.Errorf("writing lock: %w", err)
	}
	return nil
}

// v2ArtifactsFromIndex converts every platform row of one index
// version. name selects the attestation identity policy: a
// declared attestation without a gale-side policy is an error,
// because it could never be verified (§7d).
func v2ArtifactsFromIndex(
	name string, arts map[string]index.Artifact, commit string,
) (map[string]lockfile.V2Artifact, error) {
	out := make(map[string]lockfile.V2Artifact, len(arts))
	for plat, a := range arts {
		art, err := v2ArtifactFromIndex(name, a, commit)
		if err != nil {
			return nil, err
		}
		out[plat] = art
	}
	return out, nil
}

func v2ArtifactFromIndex(
	name string, a index.Artifact, commit string,
) (lockfile.V2Artifact, error) {
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
		repo, ok := attestation.PolicyFor(name)
		if !ok {
			return lockfile.V2Artifact{}, fmt.Errorf(
				"index declares an attestation for %s but gale has "+
					"no identity policy for it; refusing to lock "+
					"what cannot be verified", name)
		}
		art.Attestation = &lockfile.V2Attestation{
			Issuer: attestation.GitHubIssuer,
			SAN:    "https://github.com/" + repo,
			Repo:   repo,
		}
	}
	return art, nil
}
