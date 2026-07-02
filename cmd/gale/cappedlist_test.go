package main

import (
	"strings"
	"testing"
)

func TestCappedList(t *testing.T) {
	t.Run("fewer than max items", func(t *testing.T) {
		got := cappedList("header", []string{"a", "b"}, "")
		want := "header\n  a\n  b"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("exactly max items", func(t *testing.T) {
		items := []string{"a", "b", "c", "d", "e"}
		got := cappedList("header", items, "")
		if strings.Contains(got, "more") {
			t.Errorf("unexpected overflow line: %q", got)
		}
		if !strings.Contains(got, "\n  a") {
			t.Errorf("missing item a: %q", got)
		}
	})

	t.Run("overflow shows 5 and more line", func(t *testing.T) {
		items := []string{"a", "b", "c", "d", "e", "f", "g"}
		got := cappedList("header", items, "")
		if !strings.Contains(got, "\n  ... 2 more") {
			t.Errorf("missing overflow line: %q", got)
		}
		if strings.Contains(got, "\n  f") {
			t.Errorf("item beyond cap should not appear: %q", got)
		}
	})

	t.Run("empty footer omitted", func(t *testing.T) {
		got := cappedList("header", []string{"a"}, "")
		if got != "header\n  a" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("non-empty footer appended", func(t *testing.T) {
		got := cappedList("header", []string{"a"}, "run: gale sync")
		want := "header\n  a\n  run: gale sync"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("overflow with footer", func(t *testing.T) {
		items := []string{"a", "b", "c", "d", "e", "f"}
		got := cappedList("h", items, "fix it")
		if !strings.Contains(got, "\n  ... 1 more") {
			t.Errorf("overflow missing: %q", got)
		}
		if !strings.HasSuffix(got, "\n  fix it") {
			t.Errorf("footer missing at end: %q", got)
		}
	})
}
