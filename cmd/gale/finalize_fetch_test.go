package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/fetch"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const (
	fetchPubName    = "just"
	fetchPubVersion = "1.56.0"
	fetchPubBody    = "just-bytes\n"
)

type fetchPubFixture struct {
	t          *testing.T
	home       string
	root       string
	c          *cmdContext
	art        index.Artifact
	lock       *lockfile.V2
	prev       int
	toStore    func(context.Context, *store.Store, string, string, index.Artifact) (string, error)
	storeCalls atomic.Int32
}

func newFetchPubFixture(t *testing.T) *fetchPubFixture {
	t.Helper()
	p := newProjectLayout(t)
	seedStore(t, p.storeRoot, fetchPubName, fetchPubVersion)
	if err := generation.Build(
		map[string]string{fetchPubName: fetchPubVersion},
		p.galeDir, p.storeRoot,
	); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	body := []byte(fetchPubBody)
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	tree := mappedJustDigest(t)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(download.SetHTTPClient(srv.Client()))
	t.Cleanup(download.SetProgressEnabled(false))

	art := index.Artifact{
		URL:        srv.URL + "/" + fetchPubName,
		Format:     "binary",
		SHA256:     sha,
		TreeDigest: tree,
		Files: []index.FileEntry{{
			Src: fetchPubName, Dest: "bin/" + fetchPubName, Mode: 0o755,
		}},
	}
	lf := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{fetchPubName + "@" + fetchPubVersion}},
		},
		Packages: map[string]lockfile.V2Package{
			fetchPubName + "@" + fetchPubVersion: {
				Artifacts: map[string]lockfile.V2Artifact{
					"darwin/arm64": {
						URL:        art.URL,
						Format:     art.Format,
						SHA256:     art.SHA256,
						TreeDigest: art.TreeDigest,
						Method:     "fetch",
						Files: []lockfile.V2File{{
							Src: fetchPubName, Dest: "bin/" + fetchPubName, Mode: 0o755,
						}},
					},
				},
			},
		},
	}
	fetcher := &fetch.Fetcher{AllowHost: func(string) bool { return true }}
	fx := &fetchPubFixture{
		t:    t,
		home: p.home,
		root: p.root,
		c: &cmdContext{
			GalePath:  p.configPath,
			GaleDir:   p.galeDir,
			StoreRoot: p.storeRoot,
		},
		art:  art,
		lock: lf,
		prev: currentGen(t, p.galeDir),
	}
	fx.toStore = func(
		ctx context.Context, st *store.Store, name, version string, a index.Artifact,
	) (string, error) {
		fx.storeCalls.Add(1)
		return fetcher.ToStore(ctx, st, name, version, a)
	}
	return fx
}

func (fx *fetchPubFixture) publish() fetchPublish {
	return fetchPublish{
		Name:    fetchPubName,
		Version: fetchPubVersion,
		Art:     fx.art,
		Lock:    fx.lock,
		ToStore: fx.toStore,
	}
}

func (fx *fetchPubFixture) lockPath() string {
	fx.t.Helper()
	lp, err := lockfilePath(fx.c.GalePath)
	if err != nil {
		fx.t.Fatal(err)
	}
	return lp
}

func (fx *fetchPubFixture) fetchDest() string {
	fx.t.Helper()
	dest, err := store.NewStore(fx.c.StoreRoot).FetchPath(
		fetchPubName, fetchPubVersion, fx.art.SHA256,
	)
	if err != nil {
		fx.t.Fatal(err)
	}
	return dest
}

func (fx *fetchPubFixture) destExists() bool {
	fx.t.Helper()
	ok, err := store.NewStore(fx.c.StoreRoot).FetchExists(
		fetchPubName, fetchPubVersion, fx.art.SHA256,
	)
	if err != nil {
		fx.t.Fatalf("FetchExists: %v", err)
	}
	return ok
}

func mappedJustDigest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "bin", fetchPubName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(fetchPubBody), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := provenance.DigestTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("DigestTree: %v", err)
	}
	return digest
}

func TestFinalizeFetchHappyPathOrder(t *testing.T) {
	fx := newFetchPubFixture(t)
	var order []string
	p := fx.publish()
	p.afterStage = func() error { order = append(order, "stage"); return nil }
	p.afterRegister = func() error { order = append(order, "register"); return nil }
	p.afterWrite = func() error { order = append(order, "write"); return nil }
	p.beforeSwap = func() error { order = append(order, "swap"); return nil }

	if err := finalizeFetch(context.Background(), fx.c, p); err != nil {
		t.Fatalf("finalizeFetch: %v", err)
	}

	wantOrder := []string{"stage", "register", "write", "swap"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}
	if !fx.destExists() {
		t.Error("fetch dest missing after happy path")
	}
	if !registryContains(t, fx.home, fx.root) {
		t.Error("project was not registered")
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if got.Version != lockfile.SchemaV2 {
		t.Errorf("lock version = %d, want 2", got.Version)
	}
	if _, err := lockfile.Load(fx.lockPath()); !errors.Is(err, lockfile.ErrUnknownVersion) {
		t.Errorf("Load = %v, want ErrUnknownVersion", err)
	}
	if currentGen(t, fx.c.GaleDir) <= fx.prev {
		t.Errorf("current = %d, want > %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
	if _, err := os.Lstat(mutateLockPath(fx.c.GaleDir)); err != nil {
		t.Errorf("mutate.lock: %v", err)
	}
}

func TestFinalizeFetchRejectsConflictingLockRoots(t *testing.T) {
	fx := newFetchPubFixture(t)
	fx.lock.Targets.Default.Roots = []string{
		fetchPubName + "@" + fetchPubVersion,
		fetchPubName + "@1.57.0",
	}
	err := finalizeFetch(context.Background(), fx.c, fx.publish())
	if !errors.Is(err, lockfile.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if fx.storeCalls.Load() != 0 {
		t.Error("ToStore ran for a lock that cannot activate")
	}
	assertUnchangedPublication(t, fx)
}

func TestFinalizeFetchMutationLockExclusive(t *testing.T) {
	fx := newFetchPubFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	first := fx.publish()
	first.beforeSwap = func() error {
		close(started)
		<-release
		return nil
	}

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- finalizeFetch(context.Background(), fx.c, first)
	}()

	select {
	case <-started:
	case err := <-firstErr:
		t.Fatalf("first finalizeFetch returned before beforeSwap: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("first finalizeFetch did not reach beforeSwap")
	}

	var secondSawLock atomic.Bool
	second := fx.publish()
	second.afterLock = func() error {
		secondSawLock.Store(true)
		return nil
	}
	secondErr := make(chan error, 1)
	go func() {
		secondErr <- finalizeFetch(context.Background(), fx.c, second)
	}()

	select {
	case <-time.After(50 * time.Millisecond):
	case err := <-secondErr:
		t.Fatalf("second finalizeFetch finished while first held the lock: %v", err)
	}
	if secondSawLock.Load() {
		t.Fatal("second finalizeFetch reached afterLock while first held mutate.lock")
	}

	close(release)
	if err := <-firstErr; err != nil {
		t.Fatalf("first finalizeFetch: %v", err)
	}
	select {
	case err := <-secondErr:
		if err != nil {
			t.Fatalf("second finalizeFetch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second finalizeFetch did not finish after the first released")
	}
}

func TestFinalizeFetchDistinctScopesDoNotShareLock(t *testing.T) {
	a := newFetchPubFixture(t)
	b := newFetchPubFixture(t)
	if mutateLockPath(a.c.GaleDir) == mutateLockPath(b.c.GaleDir) {
		t.Fatal("fixtures share mutate.lock")
	}

	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() { errA <- finalizeFetch(context.Background(), a.c, a.publish()) }()
	go func() { errB <- finalizeFetch(context.Background(), b.c, b.publish()) }()

	for i, ch := range []chan error{errA, errB} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("scope %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("scope %d blocked on the other scope's lock", i)
		}
	}
}

func TestFinalizeFetchFailAfterLockBeforeStage(t *testing.T) {
	fx := newFetchPubFixture(t)
	p := fx.publish()
	p.afterLock = func() error { return errors.New("stop after lock") }

	if err := finalizeFetch(context.Background(), fx.c, p); err == nil {
		t.Fatal("finalizeFetch succeeded, want afterLock error")
	}
	if fx.storeCalls.Load() != 0 {
		t.Error("ToStore ran after afterLock failure")
	}
	if fx.destExists() {
		t.Error("dest exists; stage must not have run")
	}
	assertUnchangedPublication(t, fx)
}

func TestFinalizeFetchFailAfterStageBeforeRegister(t *testing.T) {
	fx := newFetchPubFixture(t)
	p := fx.publish()
	p.afterStage = func() error { return errors.New("stop after stage") }

	if err := finalizeFetch(context.Background(), fx.c, p); err == nil {
		t.Fatal("finalizeFetch succeeded, want afterStage error")
	}
	if !fx.destExists() {
		t.Error("dest missing; stage should have completed")
	}
	if registryContains(t, fx.home, fx.root) {
		t.Error("registry written before register step should have run")
	}
	assertUnchangedPublication(t, fx)
}

func TestFinalizeFetchRegisterIOFailure(t *testing.T) {
	fx := newFetchPubFixture(t)
	if err := os.MkdirAll(filepath.Join(fx.home, ".gale", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := finalizeFetch(context.Background(), fx.c, fx.publish()); err == nil {
		t.Fatal("finalizeFetch succeeded, want register I/O error")
	}
	assertUnchangedPublication(t, fx)
}

func TestFinalizeFetchFailAfterRegisterBeforeLockWrite(t *testing.T) {
	fx := newFetchPubFixture(t)
	p := fx.publish()
	p.afterRegister = func() error { return errors.New("stop after register") }

	if err := finalizeFetch(context.Background(), fx.c, p); err == nil {
		t.Fatal("finalizeFetch succeeded, want afterRegister error")
	}
	if !registryContains(t, fx.home, fx.root) {
		t.Error("register should have completed")
	}
	assertUnchangedPublication(t, fx)
}

func TestFinalizeFetchLockWriteIOFailure(t *testing.T) {
	fx := newFetchPubFixture(t)
	if err := os.Mkdir(fx.lockPath(), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := finalizeFetch(context.Background(), fx.c, fx.publish()); err == nil {
		t.Fatal("finalizeFetch succeeded, want lock write error")
	}
	if currentGen(t, fx.c.GaleDir) != fx.prev {
		t.Errorf("current = %d, want %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
}

func TestFinalizeFetchFailAfterLockWriteBeforeSwap(t *testing.T) {
	fx := newFetchPubFixture(t)
	p := fx.publish()
	p.afterWrite = func() error { return errors.New("stop after write") }

	if err := finalizeFetch(context.Background(), fx.c, p); err == nil {
		t.Fatal("finalizeFetch succeeded, want afterWrite error")
	}
	assertFailClosedLock(t, fx)
	if currentGen(t, fx.c.GaleDir) != fx.prev {
		t.Errorf("current = %d, want %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
}

func TestFinalizeFetchConvergesAfterFailedSwap(t *testing.T) {
	fx := newFetchPubFixture(t)
	p := fx.publish()
	p.afterWrite = func() error { return errors.New("stop after write") }
	if err := finalizeFetch(context.Background(), fx.c, p); err == nil {
		t.Fatal("injected failure did not fire")
	}
	assertFailClosedLock(t, fx)

	if err := finalizeFetch(context.Background(), fx.c, fx.publish()); err != nil {
		t.Fatalf("retry finalizeFetch: %v", err)
	}
	if currentGen(t, fx.c.GaleDir) <= fx.prev {
		t.Errorf("current = %d, want > %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
	digest, err := provenance.DigestTree(context.Background(), fx.fetchDest())
	if err != nil {
		t.Fatalf("DigestTree: %v", err)
	}
	if digest != fx.art.TreeDigest {
		t.Errorf("dest digest = %q, want %q", digest, fx.art.TreeDigest)
	}
}

func TestFinalizeFetchGlobalScopeSkipsRegister(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedStore(t, storeRoot, fetchPubName, fetchPubVersion)
	if err := generation.Build(
		map[string]string{fetchPubName: fetchPubVersion},
		galeDir, storeRoot,
	); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	base := newFetchPubFixture(t)
	c := &cmdContext{GalePath: configPath, GaleDir: galeDir, StoreRoot: storeRoot}
	prev := currentGen(t, galeDir)
	p := fetchPublish{
		Name:    fetchPubName,
		Version: fetchPubVersion,
		Art:     base.art,
		Lock:    base.lock,
		ToStore: base.toStore,
	}
	if err := finalizeFetch(context.Background(), c, p); err != nil {
		t.Fatalf("finalizeFetch: %v", err)
	}
	if registryContains(t, home, filepath.Dir(configPath)) {
		t.Error("global scope must not write the project registry")
	}
	if _, err := os.Lstat(mutateLockPath(galeDir)); err != nil {
		t.Errorf("mutate.lock: %v", err)
	}
	if currentGen(t, galeDir) <= prev {
		t.Errorf("current = %d, want > %d", currentGen(t, galeDir), prev)
	}
	lp, err := lockfilePath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockfile.ReadV2(lp); err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
}

func TestFinalizeFetchCanceledCtx(t *testing.T) {
	fx := newFetchPubFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := finalizeFetch(ctx, fx.c, fx.publish()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if fx.storeCalls.Load() != 0 {
		t.Error("ToStore ran on a canceled ctx")
	}
	assertUnchangedPublication(t, fx)
}

func TestFinalizeFetchStagesAllArtsBeforeRegister(t *testing.T) {
	fx := newFetchPubFixture(t)
	seedStore(t, fx.c.StoreRoot, "fd", "10.2.0")
	second := fx.art
	fx.lock.Targets.Default.Roots = []string{
		fetchPubName + "@" + fetchPubVersion,
		"fd@10.2.0",
	}
	fx.lock.Packages["fd@10.2.0"] = lockfile.V2Package{
		Artifacts: map[string]lockfile.V2Artifact{
			"darwin/arm64": {
				URL:        second.URL,
				Format:     second.Format,
				SHA256:     second.SHA256,
				TreeDigest: second.TreeDigest,
				Method:     "fetch",
				Files: []lockfile.V2File{{
					Src: "fd", Dest: "bin/fd", Mode: 0o755,
				}},
			},
		},
	}

	var staged []string
	var registered bool
	p := fetchPublish{
		Arts: []fetchArt{
			{Name: fetchPubName, Version: fetchPubVersion, Art: fx.art},
			{Name: "fd", Version: "10.2.0", Art: second},
		},
		Lock:    fx.lock,
		ToStore: fx.toStore,
	}
	p.afterStage = func() error {
		if registered {
			t.Error("register ran before afterStage")
		}
		return nil
	}
	p.afterRegister = func() error {
		registered = true
		return nil
	}
	p.ToStore = func(
		ctx context.Context, st *store.Store, name, version string, a index.Artifact,
	) (string, error) {
		staged = append(staged, name+"@"+version)
		return fx.toStore(ctx, st, name, version, a)
	}

	if err := finalizeFetch(context.Background(), fx.c, p); err != nil {
		t.Fatalf("finalizeFetch: %v", err)
	}
	want := []string{fetchPubName + "@" + fetchPubVersion, "fd@10.2.0"}
	if !reflect.DeepEqual(staged, want) {
		t.Errorf("staged = %v, want %v", staged, want)
	}
	if fx.storeCalls.Load() != 2 {
		t.Errorf("ToStore calls = %d, want 2", fx.storeCalls.Load())
	}
	if !registered {
		t.Error("register did not run")
	}
	if currentGen(t, fx.c.GaleDir) <= fx.prev {
		t.Errorf("current = %d, want > %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
}

func assertUnchangedPublication(t *testing.T, fx *fetchPubFixture) {
	t.Helper()
	if currentGen(t, fx.c.GaleDir) != fx.prev {
		t.Errorf("current = %d, want %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
	_, err := lockfile.ReadV2(fx.lockPath())
	if err == nil {
		t.Error("ReadV2 succeeded; lock must be unchanged")
	}
}

func assertFailClosedLock(t *testing.T, fx *fetchPubFixture) {
	t.Helper()
	if _, err := lockfile.ReadV2(fx.lockPath()); err != nil {
		t.Fatalf("ReadV2 after lock write: %v", err)
	}
	if _, err := lockfile.Load(fx.lockPath()); !errors.Is(err, lockfile.ErrUnknownVersion) {
		t.Errorf("Load = %v, want ErrUnknownVersion", err)
	}
}
