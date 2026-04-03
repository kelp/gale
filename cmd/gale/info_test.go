package main

import "testing"

func TestInfoCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "info" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'info' command")
	}
}
