package download

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ctxReader returns ctx.Err() on the next Read after cancel.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// ctxReaderAt honors ctx during zip.NewReader's central-directory
// reads, which go through ReaderAt rather than a stream.
type ctxReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (c ctxReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.ReadAt(p, off)
}

// Compiled extract caps. Tests swap the package-level copies.
const (
	defaultMaxArchiveEntries    = 1_000_000
	defaultMaxDecompressedBytes = 8 << 30
	defaultMaxCompressedBytes   = 2 << 30
)

var (
	maxArchiveEntries    = defaultMaxArchiveEntries
	maxDecompressedBytes = int64(defaultMaxDecompressedBytes)
	maxCompressedBytes   = int64(defaultMaxCompressedBytes)
)

// ErrExtractLimit reports an archive or download that exceeded a
// compiled size or entry cap.
var ErrExtractLimit = errors.New("extract limit exceeded")

// extractClass selects link and sidecar policy. Build inputs keep
// today's contract; store artifacts refuse both.
type extractClass int

const (
	classBuildInput extractClass = iota
	classArtifact
)

type extractBudget struct {
	entries int
	remain  int64
}

func newBudget() *extractBudget {
	return &extractBudget{remain: maxDecompressedBytes}
}

func (b *extractBudget) addEntry() error {
	b.entries++
	if b.entries > maxArchiveEntries {
		return fmt.Errorf("%w: too many entries", ErrExtractLimit)
	}
	return nil
}

func (b *extractBudget) reader(r io.Reader) io.Reader {
	return &capReader{r: r, remain: &b.remain, kind: "decompressed size"}
}

// capReader counts bytes against a shared remain. A read that
// would pass the cap returns ErrExtractLimit instead of EOF.
type capReader struct {
	r      io.Reader
	remain *int64
	kind   string
	hit    bool
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.hit {
		return 0, fmt.Errorf("%w: %s", ErrExtractLimit, c.kind)
	}
	if *c.remain == 0 {
		n, err := c.r.Read(p[:min(1, len(p))])
		if n > 0 {
			c.hit = true
			return 0, fmt.Errorf("%w: %s", ErrExtractLimit, c.kind)
		}
		return 0, err
	}
	if int64(len(p)) > *c.remain {
		p = p[:*c.remain]
	}
	n, err := c.r.Read(p)
	*c.remain -= int64(n)
	return n, err
}

func limitCompressed(r io.Reader) io.Reader {
	remain := maxCompressedBytes
	return &capReader{r: r, remain: &remain, kind: "compressed size"}
}

// ExtractTarGz extracts a tar.gz file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarGz(ctx context.Context, archivePath, destDir string) error {
	return extractTarFile(ctx, archivePath, destDir, classBuildInput, openGzip)
}

// ExtractTarZstd extracts a tar.zst file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarZstd(ctx context.Context, archivePath, destDir string) error {
	return extractTarFile(ctx, archivePath, destDir, classBuildInput, openZstd)
}

// ExtractTarXz extracts a tar.xz file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarXz(ctx context.Context, archivePath, destDir string) error {
	return extractTarFile(ctx, archivePath, destDir, classBuildInput, openXz)
}

// ExtractTarBz2 extracts a tar.bz2 file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractTarBz2(ctx context.Context, archivePath, destDir string) error {
	return extractTarFile(ctx, archivePath, destDir, classBuildInput, openBz2)
}

type decompressor func(io.Reader) (io.Reader, func(), error)

func openGzip(r io.Reader) (io.Reader, func(), error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("create gzip reader: %w", err)
	}
	return gr, func() { gr.Close() }, nil
}

func openZstd(r io.Reader) (io.Reader, func(), error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("create zstd reader: %w", err)
	}
	return zr, zr.Close, nil
}

func openXz(r io.Reader) (io.Reader, func(), error) {
	xr, err := xz.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("create xz reader: %w", err)
	}
	return xr, func() {}, nil
}

func openBz2(r io.Reader) (io.Reader, func(), error) {
	return bzip2.NewReader(r), func() {}, nil
}

func extractTarFile(ctx context.Context, archivePath, destDir string, class extractClass, open decompressor) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	dr, closer, err := open(ctxReader{ctx: ctx, r: f})
	if err != nil {
		return err
	}
	defer closer()

	b := newBudget()
	return extractTar(ctx, tar.NewReader(b.reader(dr)), destDir, b, class)
}

// ExtractZip extracts a zip file to destDir, preserving
// relative paths and creating directories as needed.
func ExtractZip(ctx context.Context, archivePath, destDir string) error {
	return extractZip(ctx, archivePath, destDir, classBuildInput)
}

func extractZip(ctx context.Context, archivePath, destDir string, class extractClass) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat zip archive: %w", err)
	}
	r, err := zip.NewReader(ctxReaderAt{ctx: ctx, r: f}, st.Size())
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}

	b := newBudget()
	for _, zf := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.addEntry(); err != nil {
			return err
		}
		if err := extractZipFile(ctx, destDir, zf, b, class); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(ctx context.Context, destDir string, zf *zip.File, b *extractBudget, class extractClass) error {
	target, err := archiveTarget(destDir, zf.Name)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkParent(destDir, target); err != nil {
		return fmt.Errorf("%w: %s", err, zf.Name)
	}
	if class == classArtifact && isGaleSidecar(zf.Name) {
		return forbiddenEntry(zf.Name, "gale sidecar")
	}

	mode := zf.Mode()
	if mode&os.ModeSymlink != 0 || (!zf.FileInfo().IsDir() && !mode.IsRegular()) {
		return fmt.Errorf("unsupported zip entry type for %s", zf.Name)
	}
	if zf.FileInfo().IsDir() {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", zf.Name, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", zf.Name, err)
	}
	rc, err := zf.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", zf.Name, err)
	}
	defer rc.Close()
	if err := writeFile(target, ctxReader{ctx: ctx, r: b.reader(rc)}, mode); err != nil {
		return fmt.Errorf("extract %s: %w", zf.Name, err)
	}
	return nil
}

// ExtractArtifact extracts an admitted store artifact. It refuses
// symlinks, hardlinks, and any .gale-*.toml sidecar. format is
// tar.gz, tar.xz, or zip.
func ExtractArtifact(ctx context.Context, archivePath, destDir, format string) error {
	switch format {
	case "tar.gz":
		return extractTarFile(ctx, archivePath, destDir, classArtifact, openGzip)
	case "tar.xz":
		return extractTarFile(ctx, archivePath, destDir, classArtifact, openXz)
	case "zip":
		return extractZip(ctx, archivePath, destDir, classArtifact)
	default:
		return fmt.Errorf("unsupported artifact format: %s", format)
	}
}

// ExtractSource extracts a source archive to destDir,
// detecting the format from the file extension.
func ExtractSource(ctx context.Context, archivePath, destDir string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"),
		strings.HasSuffix(archivePath, ".tgz"):
		return ExtractTarGz(ctx, archivePath, destDir)
	case strings.HasSuffix(archivePath, ".tar.xz"):
		return ExtractTarXz(ctx, archivePath, destDir)
	case strings.HasSuffix(archivePath, ".tar.bz2"):
		return ExtractTarBz2(ctx, archivePath, destDir)
	case strings.HasSuffix(archivePath, ".tar.zst"):
		return ExtractTarZstd(ctx, archivePath, destDir)
	case strings.HasSuffix(archivePath, ".zip"):
		return ExtractZip(ctx, archivePath, destDir)
	default:
		return fmt.Errorf(
			"unsupported archive format: %s", archivePath,
		)
	}
}

// ensureNoSymlinkParent verifies that no parent component of target
// (below destDir) is a symlink. extractTar may create absolute symlink
// entries verbatim, so without this check a later regular-file or
// hard-link entry could traverse such a symlink and write outside
// destDir. Components that do not yet exist are safe — MkdirAll
// creates them as real directories. Returns an error naming the
// offending path when a symlinked component is found.
func ensureNoSymlinkParent(destDir, target string) error {
	rel, err := filepath.Rel(destDir, target)
	if err != nil {
		return fmt.Errorf("resolve path against destDir: %w", err)
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	cur := destDir
	// Walk every parent component, excluding the final entry itself.
	for _, p := range parts[:len(parts)-1] {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// Component does not exist yet; it (and anything
				// deeper) will be created as real directories.
				return nil
			}
			return fmt.Errorf("stat path component %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("illegal path through symlink in archive")
		}
	}
	return nil
}

func archiveTarget(destDir, name string) (string, error) {
	target := filepath.Join(destDir, name) //nolint:gosec // G305 — path validated below
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	cleanTarget := filepath.Clean(target)
	if cleanTarget != filepath.Clean(destDir) && !strings.HasPrefix(cleanTarget, cleanDest) {
		return "", fmt.Errorf("illegal path in archive: %s", name)
	}
	return target, nil
}

// extractTar reads entries from a tar reader and extracts them
// to destDir. Validates paths to prevent directory traversal.
func extractTar(ctx context.Context, tr *tar.Reader, destDir string, b *extractBudget, class extractClass) error {
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		if err := b.addEntry(); err != nil {
			return err
		}
		if err := extractTarHeader(ctx, tr, tarDest{dir: destDir, clean: cleanDest}, hdr, class); err != nil {
			return err
		}
	}
}

type tarDest struct {
	dir, clean string
}

func extractTarHeader(ctx context.Context, tr *tar.Reader, dest tarDest, hdr *tar.Header, class extractClass) error {
	if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
		return nil
	}
	target, err := archiveTarget(dest.dir, hdr.Name)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkParent(dest.dir, target); err != nil {
		return fmt.Errorf("%w: %s", err, hdr.Name)
	}
	if class == classArtifact && isGaleSidecar(hdr.Name) {
		return forbiddenEntry(hdr.Name, "gale sidecar")
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", hdr.Name, err)
		}
		return nil
	case tar.TypeReg:
		return extractTarReg(ctx, tr, target, hdr)
	case tar.TypeSymlink:
		if class == classArtifact {
			return forbiddenEntry(hdr.Name, "symlink")
		}
		return extractTarSymlink(dest.clean, target, hdr)
	case tar.TypeLink:
		if class == classArtifact {
			return forbiddenEntry(hdr.Name, "hardlink")
		}
		return extractTarHardlink(dest.dir, dest.clean, target, hdr)
	default:
		return fmt.Errorf("unsupported tar entry type %d for %s",
			hdr.Typeflag, hdr.Name)
	}
}

func extractTarReg(ctx context.Context, tr *tar.Reader, target string, hdr *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", hdr.Name, err)
	}
	if err := writeFile(target, ctxReader{ctx: ctx, r: tr}, hdr.FileInfo().Mode()); err != nil {
		return fmt.Errorf("extract %s: %w", hdr.Name, err)
	}
	return nil
}

func extractTarSymlink(cleanDest, target string, hdr *tar.Header) error {
	// Relative symlink targets must stay inside destDir.
	// Absolute targets are written as-is (often dangling).
	// Later writes cannot follow a symlinked parent
	// (ensureNoSymlinkParent) and file writes use O_NOFOLLOW.
	if !filepath.IsAbs(hdr.Linkname) {
		resolved := filepath.Join(filepath.Dir(target), hdr.Linkname) //nolint:gosec // G305 — validated below
		resolved = filepath.Clean(resolved)
		if !strings.HasPrefix(resolved, cleanDest) {
			return fmt.Errorf("illegal symlink target in archive: %s -> %s",
				hdr.Name, hdr.Linkname)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", hdr.Name, err)
	}
	os.Remove(target)
	if err := os.Symlink(hdr.Linkname, target); err != nil {
		return fmt.Errorf("create symlink %s: %w", hdr.Name, err)
	}
	return nil
}

func extractTarHardlink(destDir, cleanDest, target string, hdr *tar.Header) error {
	linkTarget := filepath.Join(destDir, hdr.Linkname) //nolint:gosec // G305 — path validated below
	if !strings.HasPrefix(filepath.Clean(linkTarget), cleanDest) {
		return fmt.Errorf("illegal hard link target in archive: %s", hdr.Linkname)
	}
	if err := ensureNoSymlinkParent(destDir, linkTarget); err != nil {
		return fmt.Errorf("%w: %s", err, hdr.Linkname)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", hdr.Name, err)
	}
	os.Remove(target)
	if err := os.Link(linkTarget, target); err != nil {
		return fmt.Errorf("create hard link %s: %w", hdr.Name, err)
	}
	return nil
}

// writeFile creates a file at path, copies content from r,
// and sets the given file mode. Special bits (setuid/setgid/
// sticky) are dropped. fchmod makes Perm() umask-independent.
func writeFile(path string, r io.Reader, mode os.FileMode) error {
	perm := mode.Perm()
	// O_NOFOLLOW rejects a final path component that is itself a
	// symlink, so a regular-file entry sharing a name with a
	// previously extracted symlink cannot follow it and clobber a
	// target outside destDir.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// isGaleSidecar matches provenance.isGaleSidecar. download stays
// a leaf, so the two-line predicate is duplicated on purpose.
func isGaleSidecar(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasPrefix(base, ".gale-") && strings.HasSuffix(base, ".toml")
}

func forbiddenEntry(name, kind string) error {
	return fmt.Errorf("extract %s: forbidden archive entry: %s", name, kind)
}
