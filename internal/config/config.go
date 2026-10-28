// Package config loads runtime configuration from the environment. Load fails
// fast on anything required that is missing: a control plane that boots with
// a half-configured trust boundary is worse than one that does not boot.
package config

import (
	"os"
	"sort"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Config is the whole runtime configuration of odyssey-api.
type Config struct {
	Port        string
	DatabaseURL string
	IAM         IAM
	// CORSOrigins are the browser origins allowed to call the API
	// (CORS_ORIGINS, comma-separated). Empty leaves CORS closed.
	CORSOrigins []string
	// Fleet is what the API puts in enrolment tokens. Optional: without it
	// clusters can be listed but not registered.
	Fleet Fleet
}

// Fleet is the API's view of the fleet gateway.
type Fleet struct {
	// CACert is FLEET_CA_CERT: the fleet CA certificate file (agents pin
	// its key). The API never holds the CA key.
	CACert string
	// Gateway is FLEET_GATEWAY_URL: host:port agents dial.
	Gateway string
}

// Enabled reports whether clusters can be registered.
func (f Fleet) Enabled() bool { return f.CACert != "" && f.Gateway != "" }

// IAM holds the trusted token boundaries checked on every request. They come
// from configuration, never from the token itself.
type IAM struct {
	URL         string // IAMKit base URL reachable from this service
	PublicURL   string // IAMKit base URL reachable from browsers (defaults to URL)
	Issuer      string // JWT_ISSUER configured in IAMKit
	Audience    string
	Environment string
	Application string
	Resource    string // the IAMKit resource holding iam/permissions.json
	WebClientID string // public OAuth client of the dashboard (code + PKCE); optional
}

// Load reads the environment.
func Load() (Config, error) {
	cfg := Config{
		Port:        env("PORT", "8095"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		IAM: IAM{
			URL:         strings.TrimRight(os.Getenv("IAMKIT_URL"), "/"),
			PublicURL:   strings.TrimRight(env("IAMKIT_PUBLIC_URL", os.Getenv("IAMKIT_URL")), "/"),
			Issuer:      os.Getenv("IAMKIT_ISSUER"),
			Audience:    os.Getenv("IAMKIT_AUDIENCE"),
			Environment: os.Getenv("IAMKIT_ENVIRONMENT_ID"),
			Application: os.Getenv("IAMKIT_APPLICATION_ID"),
			Resource:    os.Getenv("IAMKIT_RESOURCE_ID"),
			WebClientID: os.Getenv("IAMKIT_WEB_CLIENT_ID"),
		},
		CORSOrigins: splitList(os.Getenv("CORS_ORIGINS")),
		Fleet:       Fleet{CACert: os.Getenv("FLEET_CA_CERT"), Gateway: os.Getenv("FLEET_GATEWAY_URL")},
	}
	if (cfg.Fleet.CACert == "") != (cfg.Fleet.Gateway == "") {
		return Config{}, errx.Validation("FLEET_CA_CERT and FLEET_GATEWAY_URL go together")
	}
	var missing []string
	for name, v := range map[string]string{
		"DATABASE_URL":          cfg.DatabaseURL,
		"IAMKIT_URL":            cfg.IAM.URL,
		"IAMKIT_ISSUER":         cfg.IAM.Issuer,
		"IAMKIT_AUDIENCE":       cfg.IAM.Audience,
		"IAMKIT_ENVIRONMENT_ID": cfg.IAM.Environment,
		"IAMKIT_APPLICATION_ID": cfg.IAM.Application,
		"IAMKIT_RESOURCE_ID":    cfg.IAM.Resource,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Config{}, errx.Validation("missing required environment: " + strings.Join(missing, ", "))
	}
	return cfg, nil
}

// Worker is the configuration of odyssey-worker: it needs the database only
// (it never sees a user's token).
type Worker struct {
	DatabaseURL string
	// HealthPort serves GET /health for liveness probes ("" disables it).
	HealthPort string
	// Cluster is where the deploy workflow runs apps (only the worker talks
	// to clusters).
	Cluster Cluster
}

// Cluster kinds.
const (
	ClusterFake  = "fake"  // in memory: apps "run" without a cluster (development, tests)
	ClusterFleet = "fleet" // the organization's enrolled clusters, through their agents' tunnels
)

// Cluster selects the release.Cluster adapter.
type Cluster struct {
	// Kind is ODYSSEY_CLUSTER: fake (default) or fleet.
	Kind string
}

// LoadWorker reads the worker's environment.
func LoadWorker() (Worker, error) {
	cfg := Worker{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		HealthPort:  env("WORKER_HEALTH_PORT", "8096"),
		Cluster:     Cluster{Kind: env("ODYSSEY_CLUSTER", ClusterFake)},
	}
	if cfg.DatabaseURL == "" {
		return Worker{}, errx.Validation("missing required environment: DATABASE_URL")
	}
	if cfg.Cluster.Kind != ClusterFake && cfg.Cluster.Kind != ClusterFleet {
		return Worker{}, errx.Validation("ODYSSEY_CLUSTER must be fake or fleet")
	}
	return cfg, nil
}

// Gateway is the configuration of odyssey-gateway, where agents connect.
// It holds the fleet CA key: it signs the agents' certificates and its own.
type Gateway struct {
	DatabaseURL string
	// Listen is FLEET_GATEWAY_LISTEN (default :8443).
	Listen string
	// Hosts are FLEET_GATEWAY_HOSTS: the names and IPs agents reach it by
	// (its serving certificate covers them).
	Hosts      []string
	CACert     string // FLEET_CA_CERT
	CAKey      string // FLEET_CA_KEY
	HealthPort string // GATEWAY_HEALTH_PORT (default 8098; "" disables it)
}

// LoadGateway reads the gateway's environment.
func LoadGateway() (Gateway, error) {
	cfg := Gateway{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Listen:      env("FLEET_GATEWAY_LISTEN", ":8443"),
		Hosts:       splitList(os.Getenv("FLEET_GATEWAY_HOSTS")),
		CACert:      os.Getenv("FLEET_CA_CERT"),
		CAKey:       os.Getenv("FLEET_CA_KEY"),
		HealthPort:  env("GATEWAY_HEALTH_PORT", "8098"),
	}
	var missing []string
	for name, v := range map[string]string{
		"DATABASE_URL":        cfg.DatabaseURL,
		"FLEET_CA_CERT":       cfg.CACert,
		"FLEET_CA_KEY":        cfg.CAKey,
		"FLEET_GATEWAY_HOSTS": strings.Join(cfg.Hosts, ","),
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Gateway{}, errx.Validation("missing required environment: " + strings.Join(missing, ", "))
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// splitList splits a comma-separated list, dropping blanks and trailing slashes.
func splitList(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
			out = append(out, v)
		}
	}
	return out
}
