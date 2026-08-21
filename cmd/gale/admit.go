package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kelp/gale/internal/admit"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/fetch"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/provenance"
)

// admitInspector is the host-tool seam. Tests inject a stub so
// Linux CI never shells out to otool or codesign.
var admitInspector admit.Inspector

type admitReq struct {
	Archive    string
	Name       string
	Version    string
	OS         string
	Arch       string
	URL        string
	Format     string
	HashSource string
	SHA256     string
	Strip      int
	Files      []index.FileEntry
}

var admitCmd = &cobra.Command{
	Use:   "admit",
	Short: "Record an index artifact from a local archive",
	Long: "Extract a local archive with Gale's installer path, " +
		"compute tree_digest, and print an index fragment. " +
		"Does not write the store, lock, or gale.toml.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		req, err := admitReqFrom(cmd)
		if err != nil {
			return err
		}
		return runAdmit(cmd.Context(), req, cmd.OutOrStdout())
	},
}

func init() {
	f := admitCmd.Flags()
	f.String("archive", "", "Local archive path")
	f.String("name", "", "Package name")
	f.String("version", "", "Package version")
	f.String("os", "", "Index OS (darwin or linux)")
	f.String("arch", "", "Index arch (arm64 or amd64)")
	f.String("url", "", "Recorded artifact URL (not fetched)")
	f.String("format", "", "Archive format (default: from suffix)")
	f.String("hash-source", "computed", "computed or upstream-sha256sums")
	f.String("sha256", "", "Expected archive hash (upstream-sha256sums)")
	f.Int("strip", 0, "Strip path components before mapping files")
	f.StringArray("file", nil, "File map entry SRC:DEST:MODE")
	rootCmd.AddCommand(admitCmd)
}

func admitReqFrom(cmd *cobra.Command) (admitReq, error) {
	var req admitReq
	req.Archive, _ = cmd.Flags().GetString("archive")
	req.Name, _ = cmd.Flags().GetString("name")
	req.Version, _ = cmd.Flags().GetString("version")
	req.OS, _ = cmd.Flags().GetString("os")
	req.Arch, _ = cmd.Flags().GetString("arch")
	req.URL, _ = cmd.Flags().GetString("url")
	req.Format, _ = cmd.Flags().GetString("format")
	req.HashSource, _ = cmd.Flags().GetString("hash-source")
	req.SHA256, _ = cmd.Flags().GetString("sha256")
	req.Strip, _ = cmd.Flags().GetInt("strip")
	files, _ := cmd.Flags().GetStringArray("file")
	if err := fillAdmitReq(&req, files); err != nil {
		return admitReq{}, err
	}
	return req, nil
}

func fillAdmitReq(req *admitReq, files []string) error {
	if req.Archive == "" || req.Name == "" || req.Version == "" ||
		req.OS == "" || req.Arch == "" || req.URL == "" {
		return fmt.Errorf("archive, name, version, os, arch, and url are required")
	}
	if req.Format == "" {
		format, err := admit.InferFormat(req.Archive)
		if err != nil {
			return fmt.Errorf("format: %w", err)
		}
		req.Format = format
	}
	if req.HashSource == "" {
		req.HashSource = "computed"
	}
	if req.HashSource != "computed" && req.HashSource != "upstream-sha256sums" {
		return fmt.Errorf("hash-source must be computed or upstream-sha256sums")
	}
	if req.HashSource == "upstream-sha256sums" && req.SHA256 == "" {
		return fmt.Errorf("hash-source upstream-sha256sums requires --sha256")
	}
	if len(files) == 0 {
		return fmt.Errorf("at least one --file src:dest:mode is required")
	}
	for _, raw := range files {
		fe, err := admit.ParseFileFlag(raw)
		if err != nil {
			return fmt.Errorf("file %q: %w", raw, err)
		}
		req.Files = append(req.Files, index.FileEntry{
			Src: fe.Src, Dest: fe.Dest, Mode: fe.Mode,
		})
	}
	if req.Format == "binary" {
		base := urlBase(req.URL)
		if req.Files[0].Src != base || len(req.Files) != 1 {
			return fmt.Errorf("binary src must be %s", base)
		}
	}
	return nil
}

func urlBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return path.Base(raw)
	}
	return path.Base(u.Path)
}

func runAdmit(ctx context.Context, req admitReq, w io.Writer) error {
	art := index.Artifact{
		URL:        req.URL,
		Format:     req.Format,
		HashSource: req.HashSource,
		Strip:      req.Strip,
		Files:      req.Files,
	}
	if err := fetch.ValidateSpec(art, nil); err != nil {
		return fmt.Errorf("admit spec: %w", err)
	}
	work, err := os.MkdirTemp("", "gale-admit-")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(work)
	if err := landAdmit(ctx, work, req, &art); err != nil {
		return err
	}
	if err := inspectAdmit(ctx, work, req); err != nil {
		return err
	}
	_, err = io.WriteString(w, admit.FormatFragment(req.Version, req.OS+"/"+req.Arch, art))
	return err
}

func landAdmit(ctx context.Context, work string, req admitReq, art *index.Artifact) error {
	staged := filepath.Join(work, "archive")
	if err := copyRegular(req.Archive, staged); err != nil {
		return fmt.Errorf("stage archive: %w", err)
	}
	sum, err := download.HashFile(ctx, staged)
	if err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	if req.HashSource == "upstream-sha256sums" {
		if err := download.VerifySHA256(ctx, staged, req.SHA256); err != nil {
			return fmt.Errorf("verify archive: %w", err)
		}
	}
	art.SHA256 = sum
	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return fmt.Errorf("create tree dir: %w", err)
	}
	if err := fetch.PlaceMapped(ctx, staged, tree, *art); err != nil {
		return fmt.Errorf("place artifact: %w", err)
	}
	digest, err := provenance.DigestTree(ctx, tree)
	if err != nil {
		return fmt.Errorf("tree digest: %w", err)
	}
	art.TreeDigest = digest
	return nil
}

func inspectAdmit(ctx context.Context, work string, req admitReq) error {
	insp := admitInspector
	if insp == nil {
		insp = admit.Native{}
	}
	tree := filepath.Join(work, "tree")
	if err := admit.InspectTree(ctx, tree, req.OS, req.Arch, insp); err != nil {
		return fmt.Errorf("inspect tree: %w", err)
	}
	return nil
}

func copyRegular(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	return out.Close()
}
