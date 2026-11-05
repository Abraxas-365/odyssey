// Package releasefake is a release.Cluster in memory. It stands in for the
// fleet of real clusters until the agent exists (phase 1a-a3), in local
// development and in tests: an applied app becomes ready after a few status
// polls, or fails as its Behavior says.
package releasefake

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

// Outcome is how an applied app behaves.
type Outcome struct {
	// ReadyAfter is how many Status calls report progressing before the
	// final phase (default 0: the first call reports it).
	ReadyAfter int
	// Unhealthy makes the app fail its health check instead of becoming ready.
	Unhealthy bool
	// Reject makes Apply refuse the app (a permanent, coded error).
	Reject bool
}

// Options configure the fake; zero values take the defaults.
type Options struct {
	// Behavior decides each applied app's outcome (default: DefaultBehavior).
	Behavior func(release.DesiredApp) Outcome
	// NoAccessGate makes the cluster unable to protect apps (it lacks the
	// access capability): CanProtect is false and a team or organization app
	// is refused at Apply, like a real cluster without the gate.
	NoAccessGate bool
	// NoSecrets makes the cluster unable to take managed secrets (no key).
	NoSecrets bool
	// NoAI makes the cluster unable to wire apps to the AI (its agent lacks
	// the odicea.ai capability): an app with AI is refused at Apply.
	NoAI bool
	// SecretStores are the ClusterSecretStores it offers (none: no External
	// Secrets Operator).
	SecretStores []string
	// Domain is the base of the simulated URLs (default "apps.odyssey.localhost").
	Domain string
}

// Applied is one Apply the fake accepted.
type Applied struct {
	Target release.Target
	App    release.DesiredApp
}

type app struct {
	desired release.DesiredApp
	outcome Outcome
	polls   int
}

type Cluster struct {
	opts    Options
	mu      sync.Mutex
	apps    map[string]*app
	applied []Applied
	down    error
	key     secretbox.Key
	secrets map[string]Secret
	// failures, by environment/name, as fleet's secret_failures
	failures map[string]release.SecretFailure
	revoked  bool
	// egress is what SetEgress made the cluster report (nil: the default)
	egress *release.ClusterEgress
}

func New(opts Options) *Cluster {
	if opts.Behavior == nil {
		opts.Behavior = DefaultBehavior
	}
	if opts.Domain == "" {
		opts.Domain = "apps.odyssey.localhost"
	}
	return &Cluster{opts: opts, apps: map[string]*app{}, key: mustKey(), secrets: map[string]Secret{}, failures: map[string]release.SecretFailure{}}
}

var _ release.Cluster = (*Cluster)(nil)

func key(t release.Target, name string) string { return t.Organization.String() + "/" + name }

// SetDown makes every call fail with err (nil brings the cluster back): an
// unreachable cluster, which the workflow retries.
func (c *Cluster) SetDown(err error) {
	c.mu.Lock()
	c.down = err
	c.mu.Unlock()
}

func (c *Cluster) Apply(_ context.Context, t release.Target, d release.DesiredApp) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down != nil {
		return c.down
	}
	if c.opts.NoAccessGate && (d.Access == "team" || d.Access == "organization") {
		return release.Failed(release.CodeApplyFailed, "Este clúster no puede proteger apps con inicio de sesión. Pide a TI que lo habilite.")
	}
	for _, s := range d.Secrets {
		if s.Source == release.SecretManaged && (c.opts.NoSecrets || s.KeyID != c.key.ID()) {
			return release.Failed(release.CodeApplyFailed, "El clúster de este entorno cambió su llave: vuelve a cargar el secreto "+s.Name+" en la consola.")
		}
	}
	if d.AI != nil && c.opts.NoAI {
		return release.Failed(release.CodeApplyFailed, "Este clúster todavía no puede conectar apps con la IA. Pide a TI que actualice su agente.")
	}
	k := key(t, d.Name)
	if cur, ok := c.apps[k]; ok && cur.desired.Revision >= d.Revision {
		return nil // idempotent; an older revision is ignored
	}
	o := c.opts.Behavior(d)
	if o.Reject {
		return release.Failed(release.CodeApplyFailed, "El clúster rechazó la configuración de la app.", "fake cluster: rejected")
	}
	c.apps[k] = &app{desired: d, outcome: o}
	c.applied = append(c.applied, Applied{Target: t, App: d})
	return nil
}

func (c *Cluster) CanProtect(context.Context, release.Target) (bool, error) {
	return !c.opts.NoAccessGate, nil
}

// Secrets offers the fake's key (unless NoSecrets) and its stores, and the
// environment's failed deliveries (Fail).
func (c *Cluster) Secrets(_ context.Context, t release.Target) (release.ClusterSecrets, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down != nil {
		return release.ClusterSecrets{}, c.down
	}
	if c.revoked {
		return release.ClusterSecrets{Stores: []string{}, Revoked: true}, nil
	}
	out := release.ClusterSecrets{External: len(c.opts.SecretStores) > 0, Stores: append([]string{}, c.opts.SecretStores...), AI: !c.opts.NoAI}
	if !c.opts.NoSecrets {
		out.Key = &release.ClusterKey{Cluster: FakeClusterID, KeyID: c.key.ID(), Public: c.key.Public()}
	}
	prefix := t.Environment.String() + "/"
	for k, f := range c.failures {
		if name, ok := strings.CutPrefix(k, prefix); ok {
			if out.Failures == nil {
				out.Failures = map[string]release.SecretFailure{}
			}
			out.Failures[name] = f
		}
	}
	return out, nil
}

// Fail makes the delivery of the app's secret name at version end as the
// agent reporting err would (fleet records a failure).
func (c *Cluster) Fail(t release.Target, app, name string, version int64, err string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[t.Environment.String()+"/"+name] = release.SecretFailure{App: app, Version: version, Error: err, FailedAt: time.Now().UTC()}
}

// Revoke makes the cluster revoked: it offers nothing for secrets.
func (c *Cluster) Revoke() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = true
}

// DeliverSecret opens the value as the agent would (so a test proves it was
// sealed for this cluster, environment, name and version) and keeps it.
func (c *Cluster) DeliverSecret(_ context.Context, t release.Target, d release.SecretDelivery) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down != nil {
		return c.down
	}
	if c.opts.NoSecrets {
		return errx.Business("Este clúster todavía no puede guardar secretos.").WithCode(release.CodeSecretsUnavailable)
	}
	if d.KeyID != c.key.ID() {
		return errx.Business("El clúster cambió su llave: vuelve a cargar el valor.").WithCode(release.CodeSecretKeyChanged)
	}
	value, err := c.key.Open(secretbox.Target{Cluster: FakeClusterID, Environment: t.Environment.String(), App: d.App, Name: d.Name, Version: d.Version}, d.Sealed)
	if err != nil {
		return errx.Validation("the fake cluster cannot open the value: " + err.Error())
	}
	k := key(t, d.App) + "/" + d.Name
	if old, ok := c.secrets[k]; ok && old.Version >= d.Version {
		// fleet keeps only newer versions (the agent ignores older ones)
		return errx.Conflict("a newer value of this secret is on its way").WithCode(release.CodeSecretVersionConflict)
	}
	c.secrets[k] = Secret{Version: d.Version, Value: string(value)}
	if f, ok := c.failures[t.Environment.String()+"/"+d.Name]; ok && f.Version <= d.Version {
		delete(c.failures, t.Environment.String()+"/"+d.Name)
	}
	return nil
}

// Secret is a value the fake received.
type Secret struct {
	Version int64
	Value   string
}

// Received is the value of the app's secret named name the fake holds.
func (c *Cluster) Received(t release.Target, app, name string) (Secret, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.secrets[key(t, app)+"/"+name]
	return s, ok
}

// RotateKey gives the fake a new key (its agent was reinstalled): values
// sealed to the old one must be loaded again.
func (c *Cluster) RotateKey() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key = mustKey()
}

// SealFor seals value as the console would for the app's secret.
func (c *Cluster) SealFor(t release.Target, app, name string, version int64, value string) (keyID string, sealed []byte) {
	c.mu.Lock()
	pub, id := c.key.Public(), c.key.ID()
	c.mu.Unlock()
	sealed, err := secretbox.Seal(pub, secretbox.Target{Cluster: FakeClusterID, Environment: t.Environment.String(), App: app, Name: name, Version: version}, []byte(value))
	if err != nil {
		panic(err)
	}
	return id, sealed
}

// FakeClusterID is the cluster id the fake's key is bound to.
const FakeClusterID = "00000000-0000-4000-8000-00000000fa6e"

func mustKey() secretbox.Key {
	k, err := secretbox.GenerateKey()
	if err != nil {
		panic(err)
	}
	return k
}

func (c *Cluster) Status(_ context.Context, t release.Target, name string) (release.AppStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down != nil {
		return release.AppStatus{}, c.down
	}
	a, ok := c.apps[key(t, name)]
	if !ok {
		return release.AppStatus{}, nil
	}
	s := release.AppStatus{ObservedRevision: a.desired.Revision, Phase: release.PhaseProgressing,
		URL: "https://" + name + "." + c.opts.Domain}
	if a.polls < a.outcome.ReadyAfter {
		a.polls++
		return s, nil
	}
	if a.outcome.Unhealthy {
		s.Phase, s.Reason, s.Message = release.PhaseFailed, release.FailureUnhealthy, "GET "+a.desired.HealthPath+" respondió 503."
		return s, nil
	}
	s.Phase, s.ReadyReplicas = release.PhaseReady, a.desired.Replicas
	return s, nil
}

// Applied lists what the fake accepted, in order.
func (c *Cluster) Applied() []Applied {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Applied(nil), c.applied...)
}

// Running is the image the app named name runs ("" when none).
func (c *Cluster) Running(t release.Target, name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.apps[key(t, name)]; ok {
		return a.desired.Image
	}
	return ""
}

// DefaultBehavior makes an app ready after a few polls, so the console shows
// its progress. To try failures in a running stack, name the image's
// repository after them: one containing "unhealthy" fails its health check,
// one containing "reject" is refused by the cluster.
func DefaultBehavior(d release.DesiredApp) Outcome {
	repo, _, _ := strings.Cut(d.Image, "@")
	switch {
	case strings.Contains(repo, "reject"):
		return Outcome{Reject: true}
	case strings.Contains(repo, "unhealthy"):
		return Outcome{ReadyAfter: 3, Unhealthy: true}
	}
	return Outcome{ReadyAfter: 3}
}

// ErrDown is a convenient error for SetDown.
var ErrDown = errx.External("cluster unreachable")

// SetEgress sets the egress policy the cluster reports (default: internet,
// applied), as the fleet would after TI changed it.
func (c *Cluster) SetEgress(e release.ClusterEgress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.egress = &e
}

// Egress reports the policy set with SetEgress; the open internet when none
// was. A revoked cluster has no policy.
func (c *Cluster) Egress(context.Context, release.Target) (release.ClusterEgress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.down != nil:
		return release.ClusterEgress{}, c.down
	case c.revoked:
		return release.ClusterEgress{}, nil
	case c.egress != nil:
		return *c.egress, nil
	}
	return release.ClusterEgress{Available: true, Mode: "internet", State: release.EgressApplied, Enforced: true}, nil
}
