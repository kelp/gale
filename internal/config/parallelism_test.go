package config

import "testing"

func TestResolveParallelism(t *testing.T) {
	tests := []struct {
		name string
		env  string // "" means GALE_JOBS unset
		cfg  int
		want int
	}{
		{name: "env wins over config", env: "4", cfg: 2, want: 4},
		{name: "invalid env falls through to config", env: "abc", cfg: 2, want: 2},
		{name: "env below one falls through to config", env: "0", cfg: 2, want: 2},
		{name: "config used when env unset", env: "", cfg: 5, want: 5},
		{name: "config zero falls through to default", env: "", cfg: 0, want: 8},
		{name: "negative config falls through to default", env: "", cfg: -3, want: 8},
		{name: "default when nothing set", env: "", cfg: 0, want: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv auto-restores after the test. Empty string is
			// treated as "unset" by ResolveParallelism, so setting it to
			// "" reliably neutralizes any inherited GALE_JOBS.
			t.Setenv("GALE_JOBS", tt.env)

			cfg := &AppConfig{Sync: SyncConfig{Parallelism: tt.cfg}}
			got := ResolveParallelism(cfg)
			if got != tt.want {
				t.Errorf("%s: ResolveParallelism = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}
