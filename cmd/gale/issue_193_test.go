package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/atomicfile"
	"github.com/kelp/gale/internal/output"
)

// TestInstallRefusesSymlinkedManifest is the pipeline half of gh#193.
// A dotfile manager that links ~/.gale/gale.toml at a manifest kept
// elsewhere leaves a perfectly readable file: the install read it,
// derived the new manifest from it, and then renamed a regular file
// over the link. The link was gone and the real manifest kept its old
// contents, so the next `chezmoi apply` or `stow -R` reverted every
// install made since.
//
// The link here resolves — the unresolvable shapes are covered in
// internal/atomicfile. Refusing the resolvable one is the point: a
// write replaces the contents of the entry the caller named, never
// the entry itself.
func TestInstallRefusesSymlinkedManifest(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	recipePath := writeTestRecipe(t, tmp)
	ctx := installCtx(t, tmp, "[packages]\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	writeMatchingRecipeDigest(t, filepath.Join(ctx.StoreRoot, "testpkg", "1.0.0-1"), recipePath)

	target := filepath.Join(tmp, "dotfiles", "gale.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ctx.GalePath, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ctx.GalePath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	err = installFromRecipeFile(ctx, recipePath, output.New(os.Stderr, false))
	if !errors.Is(err, atomicfile.ErrSymlink) {
		t.Errorf("install error = %v, want ErrSymlink", err)
	}

	fi, err := os.Lstat(ctx.GalePath)
	if err != nil {
		t.Fatalf("manifest path is gone: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf(
			"manifest path is now a %s, want the symlink preserved",
			fi.Mode(),
		)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the link target: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("link target was rewritten:\n%s", after)
	}
}
