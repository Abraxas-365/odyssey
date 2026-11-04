package config_test

import (
	"os"
	"path/filepath"
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

// /mcp is mounted only with its introspection client; half of one is an
// error, and the secret comes from the variable or its _FILE, not both.
func TestMCPIntrospectionClient(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		id, secret, file string
		wantSecret       string // "" with ok: not enabled
		ok               bool
	}{
		"off":             {ok: true},
		"inline":          {id: "rs", secret: "s3cret", wantSecret: "s3cret", ok: true},
		"from file":       {id: "rs", file: secretFile, wantSecret: "from-file", ok: true},
		"id only":         {id: "rs"},
		"secret only":     {secret: "s3cret"},
		"both secrets":    {id: "rs", secret: "s3cret", file: secretFile},
		"unreadable file": {id: "rs", file: filepath.Join(t.TempDir(), "missing")},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range map[string]string{"DATABASE_URL": "postgres://x", "ODYSSEY_BLOB_URL": "mem://", "IAMKIT_URL": "http://iam",
				"IAMKIT_ISSUER": "http://iam", "IAMKIT_AUDIENCE": "http://api/mcp", "IAMKIT_ENVIRONMENT_ID": "e", "IAMKIT_APPLICATION_ID": "a",
				"IAMKIT_RESOURCE_ID": "r", "IAMKIT_INTROSPECTION_CLIENT_ID": c.id, "IAMKIT_INTROSPECTION_CLIENT_SECRET": c.secret,
				"IAMKIT_INTROSPECTION_CLIENT_SECRET_FILE": c.file} {
				t.Setenv(k, v)
			}
			cfg, err := config.Load()
			if !c.ok {
				if err == nil {
					t.Fatalf("accepted %+v", cfg.MCP)
				}
				return
			}
			if err != nil || cfg.MCP.IntrospectionClientSecret != c.wantSecret || cfg.MCP.Enabled() != (c.wantSecret != "") {
				t.Fatalf("%+v %v", cfg.MCP, err)
			}
		})
	}
}
