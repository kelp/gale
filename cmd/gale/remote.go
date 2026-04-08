package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage packages on remote machines via SSH",
}

var remoteSyncCmd = &cobra.Command{
	Use:   "sync <host>",
	Short: "Sync local gale.toml to a remote host",
	Long: `Upload local gale.toml to a remote host and run gale sync.
If gale is not installed on the host, it is bootstrapped first.
The host argument supports standard SSH syntax: user@host,
hostname, or aliases from ~/.ssh/config.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRemoteSync(args[0])
	},
}

var remoteExportCmd = &cobra.Command{
	Use:   "export <host>",
	Short: "Export local gale.toml to a remote host and sync",
	Long: `Upload local gale.toml to a remote host and run gale sync.
Unlike remote sync, this skips the bootstrap check and assumes
gale is already installed on the host.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRemoteExport(args[0])
	},
}

var remoteDiffCmd = &cobra.Command{
	Use:   "diff <host>",
	Short: "Compare local and remote package lists",
	Long: `Compare local gale.toml packages with the remote host's
gale.toml. Shows packages only local, only remote, and
version mismatches.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRemoteDiff(args[0])
	},
}

// runRemoteSync checks for gale on the host, bootstraps if
// needed, uploads gale.toml, and runs gale sync.
func runRemoteSync(host string) error {
	if err := validateHost(host); err != nil {
		return err
	}
	out := newOutput()

	configPath, err := resolveConfigPath(false)
	if err != nil {
		return fmt.Errorf("resolving config: %w", err)
	}

	out.Info(fmt.Sprintf("Checking for gale on %s...", host))
	check := sshCmd(host, "gale", "--version")
	check.Stdout = nil // suppress version output
	if err := check.Run(); err != nil {
		out.Info(fmt.Sprintf(
			"gale not found on %s, bootstrapping...", host))
		if bErr := bootstrapRemote(host); bErr != nil {
			return fmt.Errorf("bootstrapping gale: %w", bErr)
		}
		out.Success(fmt.Sprintf(
			"gale installed on %s", host))
	}

	return uploadAndSync(out, configPath, host)
}

// runRemoteExport uploads gale.toml and runs sync without
// checking whether gale is installed.
func runRemoteExport(host string) error {
	if err := validateHost(host); err != nil {
		return err
	}
	out := newOutput()

	configPath, err := resolveConfigPath(false)
	if err != nil {
		return fmt.Errorf("resolving config: %w", err)
	}

	return uploadAndSync(out, configPath, host)
}

// uploadAndSync copies gale.toml to the remote host and
// runs gale sync.
func uploadAndSync(out *output.Output, configPath, host string) error {
	out.Info(fmt.Sprintf("Uploading gale.toml to %s...", host))
	scp := exec.Command("scp", "--", configPath,
		host+":~/.gale/gale.toml")
	scp.Stdin = os.Stdin
	scp.Stderr = os.Stderr
	if err := scp.Run(); err != nil {
		return fmt.Errorf("uploading config: %w", err)
	}

	out.Info(fmt.Sprintf("Running gale sync on %s...", host))
	sync := sshCmd(host,
		"PATH=$HOME/.gale/current/bin:$PATH", "gale", "sync")
	if err := sync.Run(); err != nil {
		return fmt.Errorf("remote sync: %w", err)
	}

	out.Success(fmt.Sprintf("Remote sync complete on %s", host))
	return nil
}

// bootstrapRemote installs gale on a remote host via the
// install script.
func bootstrapRemote(host string) error {
	return bootstrapCmd(host).Run()
}

// bootstrapCommit pins the install script URL to a specific
// commit to avoid fetching potentially tampered code from a
// mutable branch reference.
const bootstrapCommit = "8a9a3bcf604cfad84121f3b1e7897fb0fc170861"

// bootstrapCmd builds the SSH command for bootstrapping gale
// on a remote host. Downloads the install script to a temp
// file before executing (no curl-pipe-to-sh).
func bootstrapCmd(host string) *exec.Cmd {
	url := "https://raw.githubusercontent.com/kelp/gale/" +
		bootstrapCommit + "/scripts/install.sh"
	script := "set -e; " +
		"t=$(mktemp); " +
		"curl -fsSL " + url + " -o \"$t\"; " +
		"sh \"$t\"; " +
		"rm -f \"$t\""
	return sshCmd(host, "sh", "-c", script)
}

// runRemoteDiff compares local and remote gale.toml packages.
func runRemoteDiff(host string) error {
	if err := validateHost(host); err != nil {
		return err
	}
	out := newOutput()

	configPath, err := resolveConfigPath(false)
	if err != nil {
		return fmt.Errorf("resolving config: %w", err)
	}

	localData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading local config: %w", err)
	}

	localCfg, err := config.ParseGaleConfig(string(localData))
	if err != nil {
		return fmt.Errorf("parsing local config: %w", err)
	}

	out.Info(fmt.Sprintf(
		"Reading gale.toml from %s...", host))
	var remoteBuf bytes.Buffer
	cmd := sshCmd(host, "cat", "~/.gale/gale.toml")
	cmd.Stdout = &remoteBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("reading remote config: %w", err)
	}

	remoteCfg, err := config.ParseGaleConfig(remoteBuf.String())
	if err != nil {
		return fmt.Errorf("parsing remote config: %w", err)
	}

	names := map[string]struct{}{}
	for name := range localCfg.Packages {
		names[name] = struct{}{}
	}
	for name := range remoteCfg.Packages {
		names[name] = struct{}{}
	}

	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	var onlyLocal, onlyRemote, mismatch, match int
	for _, name := range sorted {
		localVer, inLocal := localCfg.Packages[name]
		remoteVer, inRemote := remoteCfg.Packages[name]

		switch {
		case inLocal && !inRemote:
			fmt.Printf("  + %s@%s (local only)\n",
				name, localVer)
			onlyLocal++
		case !inLocal && inRemote:
			fmt.Printf("  - %s@%s (remote only)\n",
				name, remoteVer)
			onlyRemote++
		case localVer != remoteVer:
			fmt.Printf("  ! %s local=%s remote=%s\n",
				name, localVer, remoteVer)
			mismatch++
		default:
			match++
		}
	}

	if onlyLocal == 0 && onlyRemote == 0 && mismatch == 0 {
		out.Success("Local and remote configs match.")
	} else {
		fmt.Fprintln(os.Stderr)
		out.Info(fmt.Sprintf(
			"%d matching, %d local only, %d remote only, %d version mismatch",
			match, onlyLocal, onlyRemote, mismatch))
	}

	return nil
}

// validateHost checks that host is safe to pass to SSH/SCP.
// Rejects empty strings and hosts starting with "-" to
// prevent option injection.
func validateHost(host string) error {
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("invalid host %q: must not start with -", host)
	}
	return nil
}

// sshCmd creates an exec.Command for ssh with stdio
// connected for interactive use. Uses "--" before the host
// to prevent option injection.
func sshCmd(host string, args ...string) *exec.Cmd {
	sshArgs := append([]string{"--", host}, args...)
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func init() {
	remoteCmd.AddCommand(remoteSyncCmd)
	remoteCmd.AddCommand(remoteExportCmd)
	remoteCmd.AddCommand(remoteDiffCmd)
	rootCmd.AddCommand(remoteCmd)
}
