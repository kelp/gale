package main

import "testing"

func TestPinCommandGone(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "pin" {
			t.Fatal("gale pin must not be registered")
		}
	}
}

func TestUnpinCommandGone(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "unpin" {
			t.Fatal("gale unpin must not be registered")
		}
	}
}
