package config_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/config"
)

// The API reads ODYSSEY_CLUSTER like the worker: it asks the cluster for its
// secret key and sends it sealed secrets, so a fleet API must not answer from
// the in-memory fake (2a found its key offered for a real cluster).
func TestAPICluster(t *testing.T) {
	for cluster, want := range map[string]string{"": config.ClusterFake, "fake": config.ClusterFake, "fleet": config.ClusterFleet, "k8s": ""} {
		for k, v := range map[string]string{"DATABASE_URL": "postgres://x", "ODYSSEY_BLOB_URL": "mem://", "IAMKIT_URL": "http://iam",
			"IAMKIT_ISSUER": "http://iam", "IAMKIT_AUDIENCE": "http://api/mcp", "IAMKIT_ENVIRONMENT_ID": "e", "IAMKIT_APPLICATION_ID": "a",
			"IAMKIT_RESOURCE_ID": "r", "ODYSSEY_CLUSTER": cluster, "ODYSSEY_BUILDER": ""} {
			t.Setenv(k, v)
		}
		cfg, err := config.Load()
		if want == "" {
			if err == nil {
				t.Errorf("%q: want an error, got %q", cluster, cfg.Cluster.Kind)
			}
			continue
		}
		if err != nil || cfg.Cluster.Kind != want {
			t.Errorf("%q: got %q %v, want %q", cluster, cfg.Cluster.Kind, err, want)
		}
	}
}

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

func TestWorkerDirectory(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "sa")
	if err := os.WriteFile(secretFile, []byte("ik_svc_fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name                   string
		url, env, secret, file string
		wantErr, wantEnabled   bool
		wantSecret             string
	}{
		{name: "absent: no names, no error"},
		{name: "configured", url: "http://iam:8080/", env: "env", secret: "ik_svc_x", wantEnabled: true, wantSecret: "ik_svc_x"},
		{name: "from a file", url: "http://iam", env: "env", file: secretFile, wantEnabled: true, wantSecret: "ik_svc_fromfile"},
		{name: "both", url: "http://iam", env: "env", secret: "ik_svc_x", file: secretFile, wantErr: true},
		{name: "not a service account", url: "http://iam", env: "env", secret: "ik_key_x", wantErr: true},
		{name: "no environment", url: "http://iam", secret: "ik_svc_x", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://x")
			t.Setenv("ODYSSEY_BLOB_URL", "mem://")
			t.Setenv("IAMKIT_URL", c.url)
			t.Setenv("IAMKIT_ENVIRONMENT_ID", c.env)
			t.Setenv("IAMKIT_SERVICE_ACCOUNT_SECRET", c.secret)
			t.Setenv("IAMKIT_SERVICE_ACCOUNT_SECRET_FILE", c.file)
			cfg, err := config.LoadWorker()
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v", err)
			}
			if err == nil && (cfg.Directory.Enabled() != c.wantEnabled || cfg.Directory.Secret != c.wantSecret) {
				t.Fatalf("directory %+v", cfg.Directory)
			}
			if c.name == "configured" && cfg.Directory.URL != "http://iam:8080" {
				t.Fatalf("url %q", cfg.Directory.URL)
			}
		})
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

// The access sign-in is served only with its signing key and its IAMKit
// client together; one without the other leaves it off (it is not an error:
// an installation that has not provisioned the client still starts).
func TestAccessSignIn(t *testing.T) {
	seed := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(seed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type env = map[string]string
	for name, c := range map[string]struct {
		env     env
		enabled bool
		partial bool
		bad     bool
	}{
		"off":              {},
		"inline":           {env: env{"ODYSSEY_ACCESS_SIGNING_KEY": seed, "IAMKIT_ACCESS_CLIENT_ID": "c", "IAMKIT_ACCESS_CLIENT_SECRET": "s"}, enabled: true},
		"key from file":    {env: env{"ODYSSEY_ACCESS_SIGNING_KEY_FILE": keyFile, "IAMKIT_ACCESS_CLIENT_ID": "c", "IAMKIT_ACCESS_CLIENT_SECRET": "s"}, enabled: true},
		"key only":         {env: env{"ODYSSEY_ACCESS_SIGNING_KEY": seed}, partial: true},
		"client only":      {env: env{"IAMKIT_ACCESS_CLIENT_ID": "c", "IAMKIT_ACCESS_CLIENT_SECRET": "s"}, partial: true},
		"half a client":    {env: env{"IAMKIT_ACCESS_CLIENT_ID": "c"}, bad: true},
		"short key":        {env: env{"ODYSSEY_ACCESS_SIGNING_KEY": "AAAA"}, bad: true},
		"key and its file": {env: env{"ODYSSEY_ACCESS_SIGNING_KEY": seed, "ODYSSEY_ACCESS_SIGNING_KEY_FILE": keyFile}, bad: true},
		"previous keys":    {env: env{"ODYSSEY_ACCESS_SIGNING_KEY": seed, "ODYSSEY_ACCESS_PREVIOUS_KEYS": seed + "," + seed, "IAMKIT_ACCESS_CLIENT_ID": "c", "IAMKIT_ACCESS_CLIENT_SECRET": "s"}, enabled: true},
		"bad previous key": {env: env{"ODYSSEY_ACCESS_PREVIOUS_KEYS": "nope"}, bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range map[string]string{"DATABASE_URL": "postgres://x", "ODYSSEY_BLOB_URL": "mem://", "IAMKIT_URL": "http://iam",
				"IAMKIT_ISSUER": "http://iam", "IAMKIT_AUDIENCE": "http://api/mcp", "IAMKIT_ENVIRONMENT_ID": "e", "IAMKIT_APPLICATION_ID": "a",
				"IAMKIT_RESOURCE_ID": "r"} {
				t.Setenv(k, v)
			}
			for _, k := range []string{"ODYSSEY_ACCESS_SIGNING_KEY", "ODYSSEY_ACCESS_SIGNING_KEY_FILE", "ODYSSEY_ACCESS_PREVIOUS_KEYS",
				"IAMKIT_ACCESS_CLIENT_ID", "IAMKIT_ACCESS_CLIENT_SECRET", "IAMKIT_ACCESS_CLIENT_SECRET_FILE", "ODYSSEY_ACCESS_ALLOW_HTTP"} {
				t.Setenv(k, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg, err := config.Load()
			if c.bad {
				if err == nil {
					t.Fatalf("accepted %+v", cfg.Access)
				}
				return
			}
			if err != nil || cfg.Access.Enabled() != c.enabled || cfg.Access.Partial() != c.partial {
				t.Fatalf("%+v %v", cfg.Access, err)
			}
		})
	}
}
