package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestHostConfigFrozenFields(t *testing.T) {
	typ := reflect.TypeOf(HostConfig{})
	if typ.NumField() != 1 {
		t.Fatalf("HostConfig fields = %d, want 1", typ.NumField())
	}
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			t.Fatalf("HostConfig.%s is unexported", f.Name)
		}
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			t.Fatalf("HostConfig.%s toml tag = %q, want a name", f.Name, f.Tag)
		}
		got = append(got, name)
	}
	slices.Sort(got)
	want := []string{"packages"}
	if !slices.Equal(got, want) {
		t.Fatalf("HostConfig toml names = %v, want %v", got, want)
	}
}
