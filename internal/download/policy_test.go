package download

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/httpclient"
)

func TestFetchDoesNotSendAmbientToken(t *testing.T) {
	t.Setenv("GALE_GITHUB_TOKEN", "secret-gale")
	t.Setenv("GITHUB_TOKEN", "secret-github")

	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			sawAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("ok"))
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := Fetch(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if sawAuth != "" {
		t.Errorf("Authorization = %q, want empty", sawAuth)
	}
}

func TestFetchRejectsURLCredentials(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	for _, raw := range []string{
		"https://user:pass@example.com/x",
		"https://user@example.com/x",
	} {
		err := Fetch(context.Background(), raw, dest)
		if err == nil {
			t.Errorf("Fetch(%s): want credentials error", raw)
		}
	}
}

func TestFetchWithAuthRejectsURLCredentials(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	err := FetchWithAuth(context.Background(),
		"https://user:pass@example.com/x", dest, "tok")
	if err == nil {
		t.Fatal("FetchWithAuth: want credentials error")
	}
}

func TestFetchAndExtractRejectsURLCredentials(t *testing.T) {
	_, err := FetchAndExtractTarZstd(context.Background(),
		"https://user@example.com/x", t.TempDir(), "00", "")
	if err == nil {
		t.Fatal("FetchAndExtractTarZstd: want credentials error")
	}
}

func TestFetchDoesNotFallBackToGNUMirror(t *testing.T) {
	var hosts []string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hosts = append(hosts, r.Host)
			http.Error(w, "forbidden", http.StatusForbidden)
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	// A GNU-shaped path on the test server. The deleted mirrors
	// map used to rewrite ftpmirror.gnu.org to kernel.org.
	err := Fetch(context.Background(),
		srv.URL+"/gnu/make/make-4.4.tar.gz", dest)
	if err == nil {
		t.Fatal("expected HTTP 403")
	}
	if len(hosts) != 1 {
		t.Errorf("hosts contacted = %v, want exactly the primary", hosts)
	}
}

func TestFetchFollowsSameHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/a" {
				http.Redirect(w, r, "/b", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte("body-b"))
		},
	))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := Fetch(context.Background(), srv.URL+"/a", dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "body-b" {
		t.Errorf("got %q, want %q", got, "body-b")
	}
}

func TestFetchAllowsUnlistedCrossHostRedirect(t *testing.T) {
	secondary := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("from-secondary"))
		},
	))
	defer secondary.Close()

	primary := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, secondary.URL+"/x", http.StatusFound)
		},
	))
	defer primary.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := Fetch(context.Background(), primary.URL+"/x", dest); err != nil {
		t.Fatalf("unlisted cross-host Fetch: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-secondary" {
		t.Errorf("got %q, want from-secondary", got)
	}
}

func TestFetchHonorsCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("late"))
		},
	))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "out")
	err := Fetch(ctx, srv.URL, dest)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch canceled: %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("dest should be absent after cancel")
	}
}

func TestFetchWithAuthHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := FetchWithAuth(ctx, "https://example.com/x",
		filepath.Join(t.TempDir(), "out"), "tok")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchWithAuth canceled: %v, want context.Canceled", err)
	}
}

func TestFetchAndExtractHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "dest")
	_, err := FetchAndExtractTarZstd(ctx, "http://127.0.0.1:1/x", dest, "00", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchAndExtract canceled: %v, want context.Canceled", err)
	}
	if !destIsEmpty(t, dest) {
		t.Error("destDir should be empty after cancel")
	}
}

func TestHashFileHonorsCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := HashFile(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HashFile: %v, want context.Canceled", err)
	}
}

func TestVerifySHA256HonorsCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := VerifySHA256(ctx, path, "00")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifySHA256: %v, want context.Canceled", err)
	}
}

func TestCtxReaderStopsMidCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelAfter{
		n:      8,
		cancel: cancel,
		r:      bytes.NewReader(bytes.Repeat([]byte("x"), 64)),
	}
	_, err := io.Copy(io.Discard, ctxReader{ctx: ctx, r: r})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctxReader: %v, want context.Canceled", err)
	}
}

func TestExtractHonorsCancel(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	createTarGz(t, archive, map[string]string{
		"one.txt": "aaaaaaaa",
		"two.txt": "bbbbbbbb",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ExtractTarGz(ctx, archive, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtractTarGz: %v, want context.Canceled", err)
	}
	err = ExtractZip(ctx, archive, t.TempDir())
	// zip will fail open (wrong format) or cancel; either is
	// fine for the canceled-ctx zip path below.
	_ = err

	zipPath := filepath.Join(t.TempDir(), "a.zip")
	createZip(t, zipPath, map[string]string{"one.txt": "aaaaaaaa"})
	err = ExtractZip(ctx, zipPath, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtractZip: %v, want context.Canceled", err)
	}
}

func TestFetchWithAuthFollowsCrossHostRedirectWithoutBearer(t *testing.T) {
	var secondAuth string
	secondary := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			secondAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("cdn"))
		},
	))
	defer secondary.Close()

	primary := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, secondary.URL+"/blob", http.StatusFound)
		},
	))
	defer primary.Close()

	restore := SetHTTPClient(insecureTLSClient())
	defer restore()

	dest := filepath.Join(t.TempDir(), "out")
	err := FetchWithAuth(context.Background(), primary.URL+"/v2/x/blobs/sha256:abc", dest, "secret")
	if err != nil {
		t.Fatalf("FetchWithAuth cross-host: %v", err)
	}
	if secondAuth != "" {
		t.Errorf("second request Authorization = %q, want empty", secondAuth)
	}
}

func TestFetchWithAuthRefusesHTTPSToHTTPRedirect(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("http target must not be contacted")
		},
	))
	defer httpSrv.Close()

	primary := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, httpSrv.URL+"/x", http.StatusFound)
		},
	))
	defer primary.Close()

	restore := SetHTTPClient(primary.Client())
	defer restore()

	err := FetchWithAuth(context.Background(), primary.URL+"/x",
		filepath.Join(t.TempDir(), "out"), "secret")
	if !errors.Is(err, httpclient.ErrRedirect) {
		t.Fatalf("https→http: %v, want ErrRedirect", err)
	}
}

func TestFetchAndExtractWithTokenRefusesHTTPSToHTTPRedirect(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("http target must not be contacted")
		},
	))
	defer httpSrv.Close()

	primary := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, httpSrv.URL+"/x", http.StatusFound)
		},
	))
	defer primary.Close()

	restore := SetHTTPClient(primary.Client())
	defer restore()

	_, err := FetchAndExtractTarZstd(context.Background(),
		primary.URL+"/x", t.TempDir(), "00", "secret")
	if !errors.Is(err, httpclient.ErrRedirect) {
		t.Fatalf("token-set FetchAndExtract https→http: %v, want ErrRedirect", err)
	}
}

func TestSetHTTPClientKeepsHopPolicy(t *testing.T) {
	// After a swap, unauth CheckRedirect must still be the hop
	// policy, not the swapped client's (usually nil).
	restore := SetHTTPClient(&http.Client{})
	defer restore()
	c := unauthClient()
	if c.CheckRedirect == nil {
		t.Fatal("unauthClient CheckRedirect is nil after SetHTTPClient")
	}
	via := []*http.Request{mustHTTPRequest(t, "https://github.com/x")}
	req := mustHTTPRequest(t, "https://evil.example/x")
	err := c.CheckRedirect(req, via)
	if !errors.Is(err, httpclient.ErrRedirect) {
		t.Fatalf("hop deny after SetHTTPClient: %v, want ErrRedirect", err)
	}
}

func TestSetHTTPClientWinsForTLSFetch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("tls-ok"))
		},
	))
	defer srv.Close()

	restore := SetHTTPClient(srv.Client())
	defer restore()

	dest := filepath.Join(t.TempDir(), "out")
	if err := Fetch(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("TLS Fetch after SetHTTPClient: %v", err)
	}
}

type cancelAfter struct {
	n      int
	hit    int
	cancel context.CancelFunc
	r      io.Reader
}

func (g *cancelAfter) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	g.hit += n
	if g.hit >= g.n {
		g.cancel()
	}
	return n, err
}

func mustHTTPRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func insecureTLSClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
}
