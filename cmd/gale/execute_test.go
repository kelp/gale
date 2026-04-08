package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRuntimeErrorDoesNotPrintUsage(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "boom",
		Short: "test command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("boom")
		},
	}

	var stderr bytes.Buffer
	reset := addTempRootCommand(t, cmd)
	defer reset()
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&stderr)
	rootCmd.SetArgs([]string{"boom"})

	err := executeRoot()
	if err == nil {
		t.Fatal("expected error")
	}

	out := stderr.String()
	if !strings.Contains(out, "boom") {
		t.Fatalf("stderr = %q, want runtime error", out)
	}
	if strings.Contains(out, "Usage:") {
		t.Fatalf("stderr = %q, want no usage on runtime error", out)
	}
}

func TestInvalidFlagStillPrintsUsage(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "boom",
		Short: "test command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	var stderr bytes.Buffer
	reset := addTempRootCommand(t, cmd)
	defer reset()
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&stderr)
	rootCmd.SetArgs([]string{"boom", "--badflag"})

	err := executeRoot()
	if err == nil {
		t.Fatal("expected error")
	}

	out := stderr.String()
	if !strings.Contains(out, "unknown flag") {
		t.Fatalf("stderr = %q, want unknown flag error", out)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("stderr = %q, want usage for invalid flag", out)
	}
}

func TestRuntimeErrorFormatJSON(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "boom",
		Short: "test command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("boom")
		},
	}

	var stderr bytes.Buffer
	reset := addTempRootCommand(t, cmd)
	defer reset()
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&stderr)
	rootCmd.SetArgs([]string{"--error-format=json", "boom"})

	err := executeRoot()
	if err == nil {
		t.Fatal("expected error")
	}

	var payload struct {
		Kind    string `json:"kind"`
		Command string `json:"command"`
		Message string `json:"message"`
	}
	if decErr := json.Unmarshal(stderr.Bytes(), &payload); decErr != nil {
		t.Fatalf("stderr = %q, want json: %v", stderr.String(), decErr)
	}
	if payload.Kind != "runtime" {
		t.Fatalf("kind = %q, want runtime", payload.Kind)
	}
	if payload.Command != "boom" {
		t.Fatalf("command = %q, want boom", payload.Command)
	}
	if payload.Message != "boom" {
		t.Fatalf("message = %q, want boom", payload.Message)
	}
}

func addTempRootCommand(t *testing.T, cmd *cobra.Command) func() {
	t.Helper()

	oldNoColor := noColor
	oldPlain := plain
	oldQuiet := quiet
	oldErrorFormat := errorFormat
	oldArgs := rootCmd.Flags().Args()
	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()

	rootCmd.AddCommand(cmd)
	return func() {
		rootCmd.RemoveCommand(cmd)
		noColor = oldNoColor
		plain = oldPlain
		quiet = oldQuiet
		errorFormat = oldErrorFormat
		rootCmd.SetArgs(oldArgs)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
	}
}
