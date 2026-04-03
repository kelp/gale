package main

import "testing"

func TestGenerationsCommandExists(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "generations" {
			return
		}
	}
	t.Fatal("generations command not found on rootCmd")
}

func TestGenerationsDiffSubcommand(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "generations" {
			for _, sub := range c.Commands() {
				if sub.Name() == "diff" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("diff subcommand not found on generations")
	}
}

func TestGenerationsRollbackSubcommand(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "generations" {
			for _, sub := range c.Commands() {
				if sub.Name() == "rollback" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("rollback subcommand not found on generations")
	}
}
