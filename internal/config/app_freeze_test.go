package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestAppConfigFrozenFields(t *testing.T) {
	typ := reflect.TypeOf(AppConfig{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			t.Fatalf("AppConfig.%s is unexported", f.Name)
		}
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			t.Fatalf("AppConfig.%s toml tag = %q, want a name", f.Name, f.Tag)
		}
		got = append(got, name)
	}
	slices.Sort(got)
	want := []string{"anthropic", "build", "repos"}
	if !slices.Equal(got, want) {
		t.Fatalf("AppConfig toml names = %v, want %v", got, want)
	}
}
