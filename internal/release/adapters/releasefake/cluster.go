// Package releasefake is a release.Cluster in memory. It stands in for the
// fleet of real clusters until the agent exists (phase 1a-a3), in local
// development and in tests: an applied app becomes ready after a few status
// polls, or fails as its Behavior says.
package releasefake

import (
	"context"
	"strings"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/release"
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
}

func New(opts Options) *Cluster {
	if opts.Behavior == nil {
		opts.Behavior = DefaultBehavior
	}
	if opts.Domain == "" {
		opts.Domain = "apps.odyssey.localhost"
	}
	return &Cluster{opts: opts, apps: map[string]*app{}}
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
	k := key(t, d.Name)
	if cur, ok := c.apps[k]; ok && cur.desired.Revision >= d.Revision {
		return nil // idempotent; an older revision is ignored
	}
	o := c.opts.Behavior(d)
	if o.Reject {
		return release.Failed(release.CodeApplyFailed, "El clúster rechazó la app: la imagen no se puede descargar.")
	}
	c.apps[k] = &app{desired: d, outcome: o}
	c.applied = append(c.applied, Applied{Target: t, App: d})
	return nil
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
		s.Phase, s.Message = release.PhaseFailed, "GET "+a.desired.HealthPath+" respondió 503."
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
