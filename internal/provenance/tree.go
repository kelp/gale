package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// treePrefix guards the persisted tree-digest serialization.
// Changing any byte invalidates every lock and index entry.
const treePrefix = "gale-tree-v1\n"

const hashChunk = 32 * 1024

// ErrForbiddenEntry reports a tree that is not a bindable
// regular-file set: a symlink, hardlink, special mode bit,
// non-regular node, or a nested .gale-*.toml sidecar.
var ErrForbiddenEntry = errors.New("forbidden tree entry")

// DigestTree returns the persisted tree digest of dir: sha256 of
// gale-tree-v1 plus one sorted line per regular file
// (path, 0777 mode, content hash).
//
// dir may be a symlink (macOS /var, test-symlinked-tmp). The walk
// does not follow symlinks inside the tree; those refuse. Input
// must be quiescent — this is not a hostile same-UID walker.
//
// The digest binds on-disk Perm(). writeFile does not fchmod, so
// admission and install can disagree across umasks until extract
// hardening chmods. That PR is a prerequisite for any producer.
//
// ctx is honored during each file copy, not only between entries.
func DigestTree(ctx context.Context, dir string) (string, error) {
	walkRoot, err := treeRoot(dir)
	if err != nil {
		return "", err
	}

	w := treeWalk{root: walkRoot, buf: make([]byte, hashChunk)}
	err = filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("tree digest: %w", walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == walkRoot {
			return nil
		}
		return w.entry(ctx, p, d)
	})
	if err != nil {
		return "", err
	}

	sort.Strings(w.lines)
	var b strings.Builder
	b.WriteString(treePrefix)
	for _, line := range w.lines {
		b.WriteString(line)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// treeRoot resolves dir to a real directory. A symlink root is
// allowed; a non-directory is not.
func treeRoot(dir string) (string, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("tree digest: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("tree digest %s: %w: not a directory", dir, ErrForbiddenEntry)
	}
	walkRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("tree digest: %w", err)
	}
	return filepath.Clean(walkRoot), nil
}

// treeWalk holds walk state. ctx is passed per call, never stored.
type treeWalk struct {
	root  string
	buf   []byte
	lines []string
}

func (w *treeWalk) entry(ctx context.Context, p string, d fs.DirEntry) error {
	rel, err := treeRel(w.root, p)
	if err != nil {
		return err
	}
	if d.IsDir() {
		return nil
	}
	if d.Type()&os.ModeSymlink != 0 {
		return forbidden(rel, "symlink")
	}
	if !d.Type().IsRegular() {
		return forbidden(rel, "not a regular file")
	}
	line, err := fileLine(ctx, p, rel, w.buf)
	if err != nil {
		return err
	}
	if line != "" {
		w.lines = append(w.lines, line)
	}
	return nil
}

func treeRel(root, full string) (string, error) {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("tree digest: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if err := checkRelPath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

func checkRelPath(rel string) error {
	if rel == "" || rel == "." {
		return forbidden(rel, "empty path")
	}
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, "\n") {
		return forbidden(rel, "path contains NUL or newline")
	}
	if path.IsAbs(rel) {
		return forbidden(rel, "absolute path")
	}
	for _, c := range strings.Split(rel, "/") {
		if c == ".." {
			return forbidden(rel, ".. component")
		}
	}
	return nil
}

func fileLine(ctx context.Context, full, rel string, buf []byte) (string, error) {
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return "", forbidden(rel, "symlink")
		}
		return "", fmt.Errorf("tree digest: open %s: %w", rel, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("tree digest: stat %s: %w", rel, err)
	}
	if err := checkRegular(rel, fi); err != nil {
		return "", err
	}
	if isGaleSidecar(rel) {
		if strings.Contains(rel, "/") {
			return "", forbidden(rel, "nested gale sidecar")
		}
		return "", nil
	}

	sum, err := hashOpen(ctx, f, buf)
	if err != nil {
		return "", err
	}
	if err := checkUnchanged(rel, f, fi); err != nil {
		return "", err
	}
	return fmt.Sprintf("file\x00%s\x00%04o\x00%s\n", rel, fi.Mode().Perm(), sum), nil
}

func checkRegular(rel string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return forbidden(rel, "not a regular file")
	}
	special := os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if fi.Mode()&special != 0 {
		return forbidden(rel, "setuid, setgid, or sticky")
	}
	n, err := nlinkOf(fi)
	if err != nil {
		return fmt.Errorf("tree digest %s: %w", rel, err)
	}
	if n != 1 {
		return forbidden(rel, "hardlink")
	}
	return nil
}

func checkUnchanged(rel string, f *os.File, before os.FileInfo) error {
	after, err := f.Stat()
	if err != nil {
		return fmt.Errorf("tree digest: restat %s: %w", rel, err)
	}
	if err := checkRegular(rel, after); err != nil {
		return err
	}
	if after.Mode().Perm() != before.Mode().Perm() || after.Size() != before.Size() {
		return forbidden(rel, "metadata changed during hash")
	}
	beforeN, err := nlinkOf(before)
	if err != nil {
		return fmt.Errorf("tree digest %s: %w", rel, err)
	}
	afterN, err := nlinkOf(after)
	if err != nil {
		return fmt.Errorf("tree digest %s: %w", rel, err)
	}
	if beforeN != afterN {
		return forbidden(rel, "metadata changed during hash")
	}
	return nil
}

func nlinkOf(fi os.FileInfo) (uint64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("%w: missing stat_t", ErrForbiddenEntry)
	}
	return uint64(st.Nlink), nil
}

func isGaleSidecar(rel string) bool {
	base := path.Base(rel)
	return strings.HasPrefix(base, ".gale-") && strings.HasSuffix(base, ".toml")
}

func hashOpen(ctx context.Context, f *os.File, buf []byte) (string, error) {
	h := sha256.New()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", fmt.Errorf("tree digest: hash: %w", err)
		}
	}
}

func forbidden(rel, kind string) error {
	return fmt.Errorf("tree digest %s: %w: %s", rel, ErrForbiddenEntry, kind)
}
