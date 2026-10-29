package config_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/config"
)

// A fleet worker never pairs real clusters with the fake builder: its
// placeholder images exist nowhere.
func TestWorkerBuilder(t *testing.T) {
	for _, c := range []struct {
		cluster, builder string
		want             string // "" means an error
	}{
		{"fake", "", config.BuilderFake},
		{"fake", "none", config.BuilderNone},
		{"fleet", "", config.BuilderFleet},
		{"fleet", "none", config.BuilderNone},
		{"fleet", "fleet", config.BuilderFleet},
		{"fleet", "fake", ""},
		{"fake", "fleet", ""},
		{"fake", "kaniko", ""},
	} {
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("ODYSSEY_BLOB_URL", "mem://")
		t.Setenv("ODYSSEY_CLUSTER", c.cluster)
		t.Setenv("ODYSSEY_BUILDER", c.builder)
		cfg, err := config.LoadWorker()
		if c.want == "" {
			if err == nil {
				t.Errorf("%s/%s: want an error, got %q", c.cluster, c.builder, cfg.Builder.Kind)
			}
			continue
		}
		if err != nil || cfg.Builder.Kind != c.want {
			t.Errorf("%s/%s: got %q %v, want %q", c.cluster, c.builder, cfg.Builder.Kind, err, c.want)
		}
	}
}

func TestSourceQuota(t *testing.T) {
	for raw, want := range map[string]int64{"": 0, "1": 1 << 30, "25": 25 << 30, "0": -1, "-3": -1, "1.5": -1, "10GiB": -1} {
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("ODYSSEY_BLOB_URL", "mem://")
		t.Setenv("ODYSSEY_SOURCE_QUOTA_GIB", raw)
		cfg, err := config.LoadWorker()
		if want < 0 {
			if err == nil {
				t.Errorf("%q accepted as %d", raw, cfg.Blob.SourceQuota)
			}
			continue
		}
		if err != nil || cfg.Blob.SourceQuota != want {
			t.Errorf("%q: %d %v, want %d", raw, cfg.Blob.SourceQuota, err, want)
		}
	}
}
