package index

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/kelp/gale/internal/gitutil"
	"github.com/kelp/gale/internal/httpclient"
)

// DefaultBaseURL is the compiled-in gale-recipes raw root.
// It has no ref suffix; Open appends /{commit}/index/….
const DefaultBaseURL = "https://raw.githubusercontent.com/kelp/gale-recipes"

// ErrNotFound is a missing index document (HTTP 404 or
// git show of a path that is not in the pinned commit).
var ErrNotFound = errors.New("index entry not found")

var (
	validCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
	validName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// Source is one index origin for a resolve run.
type Source struct {
	// BaseURL is the remote root without a ref. Empty uses
	// DefaultBaseURL. Tests inject an httptest URL. A config
	// file must not set this.
	BaseURL string
	// Dir is an --index checkout. When set, it wins over BaseURL.
	// Uncommitted edits are invisible: Get uses git show.
	Dir string
	// Commit is the pin. Empty means discover HEAD (Dir) or Tip.
	Commit string
	// Tip resolves a remote HEAD. Tests inject this. The
	// default is git ls-remote of kelp/gale-recipes main.
	Tip func(ctx context.Context) (string, error)
	// HTTP overrides the shared client. Nil uses httpclient.Default.
	HTTP *http.Client
}

// Session is one resolve run pinned to one commit.
type Session struct {
	Commit string

	src   Source
	http  *http.Client
	etags map[string]etagEnt
}

type etagEnt struct {
	etag string
	body []byte
}

// Open pins one commit for the run. It does not fetch a package.
func Open(ctx context.Context, src Source) (*Session, error) {
	commit, err := resolveCommit(ctx, src)
	if err != nil {
		return nil, err
	}
	if err := checkCommit(commit); err != nil {
		return nil, err
	}
	src.BaseURL = strings.TrimRight(src.BaseURL, "/")
	if src.BaseURL == "" && src.Dir == "" {
		src.BaseURL = DefaultBaseURL
	}
	hc := src.HTTP
	if hc == nil {
		hc = httpclient.Default()
	}
	return &Session{
		Commit: commit,
		src:    src,
		http:   hc,
		etags:  make(map[string]etagEnt),
	}, nil
}

func resolveCommit(ctx context.Context, src Source) (string, error) {
	if src.Commit != "" {
		return src.Commit, nil
	}
	if src.Dir != "" {
		sha, err := gitutil.Head(ctx, src.Dir)
		if err != nil {
			return "", fmt.Errorf("reading index HEAD: %w", err)
		}
		return sha, nil
	}
	tip := src.Tip
	if tip == nil {
		tip = func(ctx context.Context) (string, error) {
			return gitutil.RemoteTip(ctx, "kelp/gale-recipes", "main")
		}
	}
	sha, err := tip(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving index tip: %w", err)
	}
	return sha, nil
}

func checkCommit(sha string) error {
	if !validCommit.MatchString(sha) {
		return fmt.Errorf("index commit %q is not a 40-character hex SHA", sha)
	}
	return nil
}

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid package name %q: must match [a-z0-9][a-z0-9-]{0,63}", name)
	}
	return nil
}

// Get loads one package document at the session commit.
func (s *Session) Get(ctx context.Context, name string) (*File, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	got, err := s.read(ctx, indexPath(name))
	if err != nil {
		return nil, err
	}
	f, err := decodeIndex(name, got.body)
	if err != nil {
		return nil, err
	}
	if got.url != "" && got.etag != "" {
		s.remember(got.url, got.etag, got.body)
	}
	return f, nil
}

// Resolve returns the named version, or package.latest when
// version is empty. The Version map is a copy.
func (s *Session) Resolve(ctx context.Context, name, version string) (string, Version, error) {
	f, err := s.Get(ctx, name)
	if err != nil {
		return "", Version{}, err
	}
	if version == "" {
		version = f.Package.Latest
	}
	ver, ok := f.Versions[version]
	if !ok {
		return "", Version{}, fmt.Errorf("index %s: version %s not found", name, version)
	}
	return version, copyVersion(ver), nil
}

type indexRead struct {
	body []byte
	url  string
	etag string
}

func (s *Session) read(ctx context.Context, path string) (indexRead, error) {
	if s.src.Dir != "" {
		return s.readLocal(ctx, path)
	}
	return s.readHTTP(ctx, path)
}

func (s *Session) readLocal(ctx context.Context, path string) (indexRead, error) {
	data, err := gitutil.Show(ctx, s.src.Dir, s.Commit, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return indexRead{}, fmt.Errorf("%s: %w", path, ErrNotFound)
		}
		return indexRead{}, err
	}
	return indexRead{body: data}, nil
}

func (s *Session) readHTTP(ctx context.Context, path string) (indexRead, error) {
	url := s.src.BaseURL + "/" + s.Commit + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return indexRead{}, fmt.Errorf("index request: %w", err)
	}
	if ent, ok := s.etags[url]; ok {
		req.Header.Set("If-None-Match", ent.etag)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return indexRead{}, fmt.Errorf("fetching index: %w", err)
	}
	defer resp.Body.Close()
	return s.readHTTPResponse(url, resp)
}

func (s *Session) readHTTPResponse(url string, resp *http.Response) (indexRead, error) {
	switch resp.StatusCode {
	case http.StatusNotModified:
		ent, ok := s.etags[url]
		if !ok {
			return indexRead{}, fmt.Errorf("fetching index: 304 without a cached body")
		}
		return indexRead{body: ent.body, url: url, etag: ent.etag}, nil
	case http.StatusNotFound:
		return indexRead{}, fmt.Errorf("fetching index: %w", ErrNotFound)
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return indexRead{}, fmt.Errorf("reading index: %w", err)
		}
		return indexRead{body: body, url: url, etag: resp.Header.Get("ETag")}, nil
	default:
		return indexRead{}, fmt.Errorf("fetching index: HTTP %d", resp.StatusCode)
	}
}

func decodeIndex(name string, raw []byte) (*File, error) {
	f, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if issues := Lint(f); len(issues) > 0 {
		return nil, fmt.Errorf("linting index %s: %s", name, issues[0].Message)
	}
	if f.Package.Name != name {
		return nil, fmt.Errorf("index %s: document name is %q", name, f.Package.Name)
	}
	return f, nil
}

func (s *Session) remember(url, etag string, body []byte) {
	if etag == "" {
		return
	}
	s.etags[url] = etagEnt{etag: etag, body: body}
}

func indexPath(name string) string {
	return "index/" + name[:1] + "/" + name + ".toml"
}

func copyVersion(v Version) Version {
	out := Version{Artifacts: make(map[string]Artifact, len(v.Artifacts))}
	for plat, art := range v.Artifacts {
		art.Files = append([]FileEntry(nil), art.Files...)
		out.Artifacts[plat] = art
	}
	return out
}
