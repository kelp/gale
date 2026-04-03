package main

import (
	"strings"
	"testing"
)

// BUG-1 & BUG-2: SSH/SCP option injection via unvalidated
// host. A host starting with "-" (e.g. "-oProxyCommand=cmd")
// can inject SSH options and execute arbitrary commands.

func TestValidateHostRejectsDashPrefix(t *testing.T) {
	err := validateHost("-oProxyCommand=evil")
	if err == nil {
		t.Error("expected error for host starting with dash")
	}
}

func TestValidateHostRejectsDoubleDashOption(t *testing.T) {
	err := validateHost("--evil-option")
	if err == nil {
		t.Error("expected error for host starting with --")
	}
}

func TestValidateHostAcceptsNormalHostname(t *testing.T) {
	err := validateHost("example.com")
	if err != nil {
		t.Errorf("unexpected error for valid host: %v", err)
	}
}

func TestValidateHostAcceptsUserAtHost(t *testing.T) {
	err := validateHost("user@example.com")
	if err != nil {
		t.Errorf("unexpected error for user@host: %v", err)
	}
}

func TestValidateHostRejectsEmpty(t *testing.T) {
	err := validateHost("")
	if err == nil {
		t.Error("expected error for empty host")
	}
}

// BUG-3: curl-pipe bootstrap runs unverified code. The
// bootstrap command should not pipe curl output directly
// to sh. It should use a pinned commit URL.

func TestBootstrapCmdDoesNotPipeCurlToSh(t *testing.T) {
	cmd := bootstrapCmd("example.com")
	full := strings.Join(cmd.Args, " ")
	if strings.Contains(full, "| sh") {
		t.Errorf(
			"bootstrap should not pipe curl to sh: %s", full)
	}
}

func TestBootstrapCmdUsesPinnedCommit(t *testing.T) {
	cmd := bootstrapCmd("example.com")
	full := strings.Join(cmd.Args, " ")
	// Must not reference /main/ in the URL - should use
	// a pinned commit hash.
	if strings.Contains(full, "/main/scripts/install.sh") {
		t.Errorf(
			"bootstrap URL should use pinned commit, not main: %s",
			full)
	}
}

func TestSshCmdInsertsDashDash(t *testing.T) {
	cmd := sshCmd("example.com", "ls")
	args := cmd.Args
	// Expect: ssh -- example.com ls
	found := false
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) &&
			args[i+1] == "example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf(
			"sshCmd should insert -- before host, got args: %v",
			args)
	}
}
