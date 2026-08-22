package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

func TestInstallRefusesSource(t *testing.T) {
	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://should-not-be-fetched", SHA256: strings.Repeat("ab", 32)},
		Build: recipe.Build{
			Steps: []string{"true"},
		},
	}
	got, err := inst.Install(context.Background(), r)
	if err == nil {
		t.Fatalf("Install compiled from source: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}

func TestInstallLocalRefusesSource(t *testing.T) {
	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}
	got, err := inst.InstallLocalWithFinalize(r, t.TempDir(), nil)
	if err == nil {
		t.Fatalf("InstallLocalWithFinalize compiled from source: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}

func TestInstallRefusesBottle(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)

	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
	}
	got, err := inst.Install(context.Background(), r)
	if err == nil {
		t.Fatalf("Install poured a leftover bottle: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
	if hits != 0 {
		t.Errorf("Install hit leftover bottle URL %d times", hits)
	}
}

func TestInstallGitRefusesSource(t *testing.T) {
	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}
	got, err := inst.InstallGitWithFinalize(r, nil)
	if err == nil {
		t.Fatalf("InstallGitWithFinalize compiled from source: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}
