// Package config loads runtime configuration from the environment. Load fails
// fast on anything required that is missing: a control plane that boots with
// a half-configured trust boundary is worse than one that does not boot.
package config

import (
	"encoding/base64"
	"errors"
	"os"
	"sort"
	"strconv"
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
	// Blob is where uploaded sources and build logs are stored.
	Blob Blob
	// Cluster is ODYSSEY_CLUSTER, as the worker's: the API asks the cluster
	// for its secret key, sends it sealed secrets and checks it can protect
	// apps (the worker applies the deployments). Both must agree.
	Cluster Cluster
	// Builder reads live build logs (the worker runs the builds); it follows
	// ODYSSEY_CLUSTER like the worker's.
	Builder Builder
	// GitHub is the App that connects repositories (optional).
	GitHub GitHub
	// MCP is the endpoint coding agents connect to (optional).
	MCP MCP
	// AI is the AI gateway apps can be given keys of (optional).
	AI AI
	// Access is the sign-in of apps protected with an Odicea login (optional).
	Access Access
	// Directory names the people of the activity trail (the IAMKit service
	// account, as the worker's; optional: without it names stay empty).
	Directory Directory
	// ConsoleURL is ODYSSEY_CONSOLE_URL: the console's base URL, for links
	// (MCP answers, the GitHub setup callback, commit statuses).
	ConsoleURL string
}

// MCP is POST /mcp, an OAuth protected resource of IAMKit: its audience is
// IAM.Audience (https://<odicea>/mcp), and a confidential client of the
// environment introspects every token (revocation within seconds). Without
// the client the endpoint is not mounted.
type MCP struct {
	// IntrospectionClientID is IAMKIT_INTROSPECTION_CLIENT_ID.
	IntrospectionClientID string
	// IntrospectionClientSecret is IAMKIT_INTROSPECTION_CLIENT_SECRET, or
	// the contents of IAMKIT_INTROSPECTION_CLIENT_SECRET_FILE.
	IntrospectionClientSecret string
	// Clients are the assistants the console offers to connect
	// (ODYSSEY_MCP_CLIENTS, comma-separated keys: claude_code, codex, cursor,
	// vscode, antigravity). Unknown keys are ignored by the assistant API.
	Clients []string
	// TemplateClients are the clients IAMKit registered for the assistants
	// with no metadata document (IAMKIT_MCP_CLIENT_<KEY>_ID, key lowercased:
	// cursor, antigravity): their configs carry the id, and it tells their
	// deployments apart. A template assistant without one is not offered.
	TemplateClients map[string]string
}

// templateClientKeys are the assistants read into MCP.TemplateClients
// (assistant.Templates).
var templateClientKeys = []string{"cursor", "antigravity"}

// DefaultMCPClients is what ODYSSEY_MCP_CLIENTS offers when unset.
const DefaultMCPClients = "claude_code,codex,cursor,vscode,antigravity"

// Enabled reports whether /mcp is served.
func (m MCP) Enabled() bool { return m.IntrospectionClientID != "" }

func loadMCP() (MCP, error) {
	m := MCP{IntrospectionClientID: os.Getenv("IAMKIT_INTROSPECTION_CLIENT_ID"), IntrospectionClientSecret: os.Getenv("IAMKIT_INTROSPECTION_CLIENT_SECRET"),
		Clients: splitList(env("ODYSSEY_MCP_CLIENTS", DefaultMCPClients)), TemplateClients: map[string]string{}}
	for _, k := range templateClientKeys {
		if id := strings.TrimSpace(os.Getenv("IAMKIT_MCP_CLIENT_" + strings.ToUpper(k) + "_ID")); id != "" {
			m.TemplateClients[k] = id
		}
	}
	if file := os.Getenv("IAMKIT_INTROSPECTION_CLIENT_SECRET_FILE"); file != "" {
		if m.IntrospectionClientSecret != "" {
			return MCP{}, errx.Validation("set IAMKIT_INTROSPECTION_CLIENT_SECRET or its _FILE, not both")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return MCP{}, errx.Validation("IAMKIT_INTROSPECTION_CLIENT_SECRET_FILE cannot be read")
		}
		m.IntrospectionClientSecret = strings.TrimSpace(string(raw))
	}
	if (m.IntrospectionClientID == "") != (m.IntrospectionClientSecret == "") {
		return MCP{}, errx.Validation("IAMKIT_INTROSPECTION_CLIENT_ID and its secret go together")
	}
	return m, nil
}

// Access is the sign-in the access gate of a cluster sends browsers to
// (docs/access.md): the API signs short tickets (Ed25519) after an IAMKit
// login. It needs the signing key and the confidential IAMKit client together
// (Enabled); with only one of them it stays off, which bootstrap logs, so an
// installation that has not provisioned the client yet still starts.
type Access struct {
	// SigningKey is the Ed25519 seed (32 bytes) of ODYSSEY_ACCESS_SIGNING_KEY
	// (base64), or the contents of ODYSSEY_ACCESS_SIGNING_KEY_FILE.
	SigningKey []byte
	// PreviousKeys are the seeds of keys that signed before
	// (ODYSSEY_ACCESS_PREVIOUS_KEYS, comma-separated base64): only their
	// public halves are published, so gates with a cached key set still verify.
	PreviousKeys [][]byte
	// ClientID and ClientSecret are IAMKIT_ACCESS_CLIENT_ID/_SECRET (or
	// IAMKIT_ACCESS_CLIENT_SECRET_FILE): the confidential authorization-code
	// client whose redirect is <API>/access/callback.
	ClientID     string
	ClientSecret string
	// AllowHTTP is ODYSSEY_ACCESS_ALLOW_HTTP=true: apps on plain http are
	// accepted for any host (without it, only localhost, *.localhost,
	// 127.0.0.1 and *.k3d.internal), and so is a plain-http IAMKIT_URL on a
	// public host name.
	AllowHTTP bool
}

// Partial reports whether exactly one of the key and the client is set.
func (a Access) Partial() bool { return (len(a.SigningKey) > 0) != (a.ClientID != "") }

// Enabled reports whether the access sign-in is served.
func (a Access) Enabled() bool { return len(a.SigningKey) > 0 && a.ClientID != "" }

func loadAccess() (Access, error) {
	a := Access{ClientID: strings.TrimSpace(os.Getenv("IAMKIT_ACCESS_CLIENT_ID")), ClientSecret: os.Getenv("IAMKIT_ACCESS_CLIENT_SECRET"),
		AllowHTTP: os.Getenv("ODYSSEY_ACCESS_ALLOW_HTTP") == "true"}
	if file := os.Getenv("IAMKIT_ACCESS_CLIENT_SECRET_FILE"); file != "" {
		if a.ClientSecret != "" {
			return Access{}, errx.Validation("set IAMKIT_ACCESS_CLIENT_SECRET or its _FILE, not both")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return Access{}, errx.Validation("IAMKIT_ACCESS_CLIENT_SECRET_FILE cannot be read")
		}
		a.ClientSecret = strings.TrimSpace(string(raw))
	}
	key := strings.TrimSpace(os.Getenv("ODYSSEY_ACCESS_SIGNING_KEY"))
	if file := os.Getenv("ODYSSEY_ACCESS_SIGNING_KEY_FILE"); file != "" {
		if key != "" {
			return Access{}, errx.Validation("set ODYSSEY_ACCESS_SIGNING_KEY or its _FILE, not both")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return Access{}, errx.Validation("ODYSSEY_ACCESS_SIGNING_KEY_FILE cannot be read")
		}
		key = strings.TrimSpace(string(raw))
	}
	if key != "" {
		seed, err := ParseAccessSeed(key)
		if err != nil {
			return Access{}, errx.Validation("ODYSSEY_ACCESS_SIGNING_KEY " + err.Error())
		}
		a.SigningKey = seed
	}
	for _, k := range splitList(os.Getenv("ODYSSEY_ACCESS_PREVIOUS_KEYS")) {
		seed, err := ParseAccessSeed(k)
		if err != nil {
			return Access{}, errx.Validation("ODYSSEY_ACCESS_PREVIOUS_KEYS " + err.Error())
		}
		a.PreviousKeys = append(a.PreviousKeys, seed)
	}
	if (a.ClientID == "") != (a.ClientSecret == "") {
		return Access{}, errx.Validation("IAMKIT_ACCESS_CLIENT_ID and its secret go together")
	}
	return a, nil
}

// ParseAccessSeed decodes a base64 Ed25519 seed (32 bytes).
func ParseAccessSeed(raw string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(strings.TrimSpace(raw)); err == nil {
			if len(b) != 32 {
				return nil, errors.New("must be a base64 Ed25519 seed of 32 bytes")
			}
			return b, nil
		}
	}
	return nil, errors.New("must be a base64 Ed25519 seed of 32 bytes")
}

// Blob is the object storage shared by the API and the worker.
type Blob struct {
	// URL is ODYSSEY_BLOB_URL (gocloudblob: s3://…, file://…). Required.
	URL string
	// SourceQuota is ODYSSEY_SOURCE_QUOTA_GIB in bytes: the compressed
	// sources an organization keeps at once (0 is the default, 10 GiB).
	SourceQuota int64
}

// Builder kinds.
const (
	BuilderFake  = "fake"  // builds nothing: an image appears after a scripted log (development, tests)
	BuilderNone  = "none"  // builds nothing: every build is refused (NO_BUILDER)
	BuilderFleet = "fleet" // the environment's cluster builds, through its agent (a fleet worker's default)
)

// Builder selects the build.Runner adapter (ODYSSEY_BUILDER). The API reads
// live logs through it and the worker runs the builds with it, so both
// resolve it the same way from the same environment: unset, it is fleet
// with ODYSSEY_CLUSTER=fleet and fake otherwise.
type Builder struct {
	Kind string
}

func loadStorage() (Blob, Builder, error) {
	b := Blob{URL: os.Getenv("ODYSSEY_BLOB_URL")}
	cluster := env("ODYSSEY_CLUSTER", ClusterFake)
	r := Builder{Kind: os.Getenv("ODYSSEY_BUILDER")}
	if b.URL == "" {
		return b, r, errx.Validation("missing required environment: ODYSSEY_BLOB_URL")
	}
	if cluster != ClusterFake && cluster != ClusterFleet {
		return b, r, errx.Validation("ODYSSEY_CLUSTER must be fake or fleet")
	}
	// The fake builder's images exist nowhere: a real cluster would be sent
	// apps it cannot pull. The fleet builder needs the fleet's clusters.
	switch r.Kind {
	case "":
		r.Kind = BuilderFake
		if cluster == ClusterFleet {
			r.Kind = BuilderFleet
		}
	case BuilderFake:
		if cluster == ClusterFleet {
			return b, r, errx.Validation("ODYSSEY_BUILDER=fake cannot build for ODYSSEY_CLUSTER=fleet: its images exist nowhere (use fleet or none)")
		}
	case BuilderFleet:
		if cluster != ClusterFleet {
			return b, r, errx.Validation("ODYSSEY_BUILDER=fleet builds in the fleet's clusters: it needs ODYSSEY_CLUSTER=fleet")
		}
	case BuilderNone:
	default:
		return b, r, errx.Validation("ODYSSEY_BUILDER must be fake, none or fleet")
	}
	if raw := os.Getenv("ODYSSEY_SOURCE_QUOTA_GIB"); raw != "" {
		gib, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || gib < 1 || gib > 1<<20 {
			return b, r, errx.Validation("ODYSSEY_SOURCE_QUOTA_GIB must be a whole number of GiB, 1 or more")
		}
		b.SourceQuota = gib << 30
	}
	return b, r, nil
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
		Cluster:     Cluster{Kind: env("ODYSSEY_CLUSTER", ClusterFake)},
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
	var err error
	if cfg.Blob, cfg.Builder, err = loadStorage(); err != nil {
		return Config{}, err
	}
	if cfg.GitHub, err = loadGitHub(); err != nil {
		return Config{}, err
	}
	if cfg.MCP, err = loadMCP(); err != nil {
		return Config{}, err
	}
	if cfg.Access, err = loadAccess(); err != nil {
		return Config{}, err
	}
	if cfg.AI, err = loadAI(); err != nil {
		return Config{}, err
	}
	if cfg.Directory, err = loadDirectory(); err != nil {
		return Config{}, err
	}
	cfg.ConsoleURL = cfg.GitHub.ConsoleURL
	return cfg, nil
}

// GitHub is Odyssey's GitHub App (phase 1c). One App per Odyssey install;
// its secrets are files (GITHUB_*_FILE), never environment values or rows.
// Unset (no GITHUB_APP_ID), GitHub is off: the console says so and every
// GitHub endpoint answers GITHUB_NOT_CONFIGURED.
type GitHub struct {
	AppID int64  // GITHUB_APP_ID
	Slug  string // GITHUB_APP_SLUG: the App's name in github.com/apps/<slug>
	// PrivateKeyFile signs the App's JWTs (PEM, RSA).
	PrivateKeyFile string // GITHUB_APP_PRIVATE_KEY_FILE
	// WebhookSecretFile holds the secret that signs webhook deliveries.
	WebhookSecretFile string // GITHUB_WEBHOOK_SECRET_FILE
	// ClientID and ClientSecretFile verify, by OAuth, who came back from
	// installing the App.
	ClientID         string // GITHUB_CLIENT_ID
	ClientSecretFile string // GITHUB_CLIENT_SECRET_FILE
	// APIURL is GITHUB_API_URL (default https://api.github.com; GitHub
	// Enterprise Server: https://<host>/api/v3); WebURL is GITHUB_URL
	// (default https://github.com).
	APIURL string
	WebURL string
	// ConsoleURL is ODYSSEY_CONSOLE_URL: where the setup callback sends the
	// browser back, and what commit statuses link to.
	ConsoleURL string
	// StateKeyFile is ODYSSEY_STATE_KEY_FILE: the key that signs the
	// install state (default: derived from the webhook secret file).
	StateKeyFile string
}

// Enabled reports whether the App is configured.
func (g GitHub) Enabled() bool { return g.AppID != 0 }

func loadGitHub() (GitHub, error) {
	g := GitHub{
		Slug:              os.Getenv("GITHUB_APP_SLUG"),
		PrivateKeyFile:    os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"),
		WebhookSecretFile: os.Getenv("GITHUB_WEBHOOK_SECRET_FILE"),
		ClientID:          os.Getenv("GITHUB_CLIENT_ID"),
		ClientSecretFile:  os.Getenv("GITHUB_CLIENT_SECRET_FILE"),
		APIURL:            strings.TrimRight(env("GITHUB_API_URL", "https://api.github.com"), "/"),
		WebURL:            strings.TrimRight(env("GITHUB_URL", "https://github.com"), "/"),
		ConsoleURL:        strings.TrimRight(env("ODYSSEY_CONSOLE_URL", "http://localhost:5178"), "/"),
		StateKeyFile:      os.Getenv("ODYSSEY_STATE_KEY_FILE"),
	}
	raw := os.Getenv("GITHUB_APP_ID")
	if raw == "" {
		return g, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return g, errx.Validation("GITHUB_APP_ID must be the App's numeric id")
	}
	g.AppID = id
	var missing []string
	for name, v := range map[string]string{
		"GITHUB_APP_SLUG":             g.Slug,
		"GITHUB_APP_PRIVATE_KEY_FILE": g.PrivateKeyFile,
		"GITHUB_WEBHOOK_SECRET_FILE":  g.WebhookSecretFile,
		"GITHUB_CLIENT_ID":            g.ClientID,
		"GITHUB_CLIENT_SECRET_FILE":   g.ClientSecretFile,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return g, errx.Validation("GITHUB_APP_ID is set; also required: " + strings.Join(missing, ", "))
	}
	return g, nil
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
	// Blob stores sources and build logs; Builder runs the builds.
	Blob    Blob
	Builder Builder
	// GitHub imports pushed commits and reports deployments on them.
	GitHub GitHub
	// AI is the AI gateway the ai workflows make and revoke keys at.
	AI AI
	// Directory names who asked for each deployment (the deployment.started
	// subscriber runs here).
	Directory Directory
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
	var err error
	if cfg.Blob, cfg.Builder, err = loadStorage(); err != nil {
		return Worker{}, err
	}
	if cfg.GitHub, err = loadGitHub(); err != nil {
		return Worker{}, err
	}
	if cfg.AI, err = loadAI(); err != nil {
		return Worker{}, err
	}
	if cfg.Directory, err = loadDirectory(); err != nil {
		return Worker{}, err
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
	// BlobURL is ODYSSEY_BLOB_URL: the store the API uploads build sources
	// to, which agents fetch them from through the gateway (read only).
	BlobURL string
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
		BlobURL:     os.Getenv("ODYSSEY_BLOB_URL"),
	}
	var missing []string
	for name, v := range map[string]string{
		"DATABASE_URL":        cfg.DatabaseURL,
		"FLEET_CA_CERT":       cfg.CACert,
		"FLEET_CA_KEY":        cfg.CAKey,
		"FLEET_GATEWAY_HOSTS": strings.Join(cfg.Hosts, ","),
		"ODYSSEY_BLOB_URL":    cfg.BlobURL,
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

// AI is the organization's AI gateway (freerouter, 2d-ai). Without a URL and
// a key the capability is absent: apps cannot turn the AI on and the console
// says so.
type AI struct {
	// URL is ODYSSEY_FREEROUTER_URL: freerouter's API, where the platform
	// makes and revokes the apps' keys.
	URL string
	// AppURL is ODYSSEY_FREEROUTER_APP_URL: the address the apps reach the
	// gateway by (default URL). It is what OPENAI_BASE_URL (+ /v1) and
	// ANTHROPIC_BASE_URL are built from.
	AppURL string
	// Key is ODYSSEY_FREEROUTER_KEY (or the contents of its _FILE): the
	// platform's service key at freerouter.
	Key string
}

// Enabled: the gateway is configured.
func (a AI) Enabled() bool { return a.URL != "" && a.Key != "" }

func loadAI() (AI, error) {
	a := AI{URL: strings.TrimRight(strings.TrimSpace(os.Getenv("ODYSSEY_FREEROUTER_URL")), "/"), Key: strings.TrimSpace(os.Getenv("ODYSSEY_FREEROUTER_KEY"))}
	a.AppURL = strings.TrimRight(strings.TrimSpace(env("ODYSSEY_FREEROUTER_APP_URL", a.URL)), "/")
	if file := os.Getenv("ODYSSEY_FREEROUTER_KEY_FILE"); file != "" {
		if a.Key != "" {
			return AI{}, errx.Validation("set ODYSSEY_FREEROUTER_KEY or its _FILE, not both")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return AI{}, errx.Validation("ODYSSEY_FREEROUTER_KEY_FILE cannot be read")
		}
		a.Key = strings.TrimSpace(string(raw))
	}
	if (a.URL == "") != (a.Key == "") {
		return AI{}, errx.Validation("ODYSSEY_FREEROUTER_URL and ODYSSEY_FREEROUTER_KEY go together")
	}
	return a, nil
}

// Directory is the organization's member list in IAMKit, read with the
// backend's service account (iam:members:read, provisioned by make seed /
// iam-provision). Without the secret deployments keep no requester name.
type Directory struct {
	// URL is IAMKIT_URL; Environment is IAMKIT_ENVIRONMENT_ID.
	URL, Environment string
	// Secret is IAMKIT_SERVICE_ACCOUNT_SECRET (or its _FILE): ik_svc_….
	Secret string
}

// Enabled: the service account is configured.
func (d Directory) Enabled() bool { return d.Secret != "" }

func loadDirectory() (Directory, error) {
	d := Directory{URL: strings.TrimRight(os.Getenv("IAMKIT_URL"), "/"), Environment: os.Getenv("IAMKIT_ENVIRONMENT_ID"),
		Secret: strings.TrimSpace(os.Getenv("IAMKIT_SERVICE_ACCOUNT_SECRET"))}
	if file := os.Getenv("IAMKIT_SERVICE_ACCOUNT_SECRET_FILE"); file != "" {
		if d.Secret != "" {
			return Directory{}, errx.Validation("set IAMKIT_SERVICE_ACCOUNT_SECRET or its _FILE, not both")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return Directory{}, errx.Validation("IAMKIT_SERVICE_ACCOUNT_SECRET_FILE cannot be read")
		}
		d.Secret = strings.TrimSpace(string(raw))
	}
	if d.Secret == "" {
		return d, nil
	}
	if !strings.HasPrefix(d.Secret, "ik_svc_") {
		return Directory{}, errx.Validation("IAMKIT_SERVICE_ACCOUNT_SECRET is not a service account secret (ik_svc_…)")
	}
	if d.URL == "" || d.Environment == "" {
		return Directory{}, errx.Validation("IAMKIT_SERVICE_ACCOUNT_SECRET needs IAMKIT_URL and IAMKIT_ENVIRONMENT_ID")
	}
	return d, nil
}
