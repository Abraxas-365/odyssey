// Package agent is Odyssey's data plane: the operator that runs in each
// cluster and reconciles the desired state the control plane sends (today
// OdysseyApps). It never imports the control plane (internal/): the two share
// only agent/api (architecture test).
package agent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/Abraxas-365/odyssey/agent/access"
	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/app"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/Abraxas-365/odyssey/agent/controllers/resource"
	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/agent/secretstore"
	"github.com/Abraxas-365/odyssey/agent/tunnel"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Options configure the agent (flags, with environment overrides).
type Options struct {
	App         app.Config
	HealthGrace time.Duration
	// HealthAddr and MetricsAddr: "0" turns them off.
	HealthAddr     string
	MetricsAddr    string
	LeaderElection bool
	// LeaderElectionNamespace holds the lease when running out of the
	// cluster (in-cluster it is the agent's own namespace).
	LeaderElectionNamespace string
	// Tunnel connects the agent to the control plane. Zero (no Store):
	// the agent only reconciles OdysseyApps written by someone else.
	Tunnel tunnel.Options
	// Secret is the agent's Secret (namespace/name): its tunnel Store when
	// Tunnel.Store is nil. Empty: no tunnel.
	Secret client.ObjectKey
	// Builds make the agent its cluster's builder (needs the tunnel).
	Builds BuildOptions
	// Access says whether the cluster has the sign-in gate (odyssey-agent
	// access, agent/access) and how Traefik reaches it.
	Access AccessOptions
	// ServiceAccount is the agent's own (namespace/name, ODYSSEY_SERVICE_ACCOUNT
	// in its namespace). It is bound to desired.EnvironmentRole in each
	// environment namespace, so the agent may keep app Secrets and erase
	// resource data there and only there. Empty (an agent run outside the
	// cluster): nothing is bound and its own credentials decide.
	ServiceAccount client.ObjectKey
}

// AccessOptions configure protected apps (access mode team/organization).
type AccessOptions struct {
	// Enabled (ODYSSEY_ACCESS_ENABLED) says the gate is installed and
	// configured (its own ODYSSEY_ACCESS_ISSUER is set). Without it, apps
	// that ask for sign-in fail with reason config instead of being exposed,
	// and the agent does not report the odicea.access capability.
	Enabled bool
	// VerifyURL is the gate's /verify endpoint as Traefik reaches it.
	VerifyURL string
	// KeyNamespace and KeyName: the Secret that holds the gate's cookie
	// key. The agent creates it (the gate only reads it).
	KeyNamespace, KeyName string
}

// Ready says protected apps can be served: the gate is enabled and its
// configuration is usable. Otherwise the agent fails closed and does not
// list the "access" feature.
func (a AccessOptions) Ready() bool {
	u, err := url.Parse(a.VerifyURL)
	return a.Enabled && err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.RawQuery == "" && a.KeyName != "" && a.KeyNamespace != ""
}

// ensureAccessKey creates the gate's cookie-key Secret (once; it never
// replaces an existing one), retrying until it works.
type ensureAccessKey struct {
	cfg *rest.Config
	o   AccessOptions
}

func (e ensureAccessKey) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("access")
	kc, err := kubernetes.NewForConfig(e.cfg)
	if err != nil {
		return err
	}
	for {
		err := access.EnsureKey(ctx, kc, e.o.KeyNamespace, e.o.KeyName)
		if err == nil {
			return nil
		}
		log.Error(err, "cannot create the gate's cookie key; retrying")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
}

// bindEnvironment binds desired.EnvironmentRole in every environment namespace
// once the agent leads: an agent upgraded from one that bound an older role
// keeps working in the namespaces it made before, and a binding someone
// deleted comes back on restart.
type bindEnvironment struct{ n *desired.Namespaces }

func (b bindEnvironment) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("environment-rbac")
	for {
		err := b.n.BindAll(ctx)
		if err == nil {
			return nil
		}
		log.Error(err, "cannot bind the environment role in every environment namespace; retrying")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
}

// hasTraefik says whether the cluster serves traefik.io/v1alpha1 now.
func hasTraefik(cfg *rest.Config) bool { return hasAPI(cfg, "traefik.io/v1alpha1") }

// hasAPI says whether the cluster serves the group version ("group/version") now.
func hasAPI(cfg *rest.Config, groupVersion string) bool {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false
	}
	_, err = dc.ServerResourcesForGroupVersion(groupVersion)
	return err == nil
}

// Version is the agent's version (set at build time with -ldflags).
var Version = "dev"

// Scheme knows Kubernetes' built-in types and Odyssey's resources.
func Scheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Run parses the command line and runs the agent until SIGINT/SIGTERM.
func Run(args []string) error {
	fs := flag.NewFlagSet("odyssey-agent", flag.ContinueOnError)
	var o Options
	fs.StringVar(&o.App.Domain, "domain", env("ODYSSEY_APPS_DOMAIN", "apps.odyssey.localhost"), "base domain of the apps' hosts")
	fs.StringVar(&o.App.URLScheme, "url-scheme", env("ODYSSEY_APPS_URL_SCHEME", "http"), "scheme of the reported app URLs")
	fs.IntVar(&o.App.URLPort, "url-port", envInt("ODYSSEY_APPS_URL_PORT", 0), "port of the reported app URLs (0 = default port)")
	fs.StringVar(&o.App.IngressClass, "ingress-class", os.Getenv("ODYSSEY_INGRESS_CLASS"), "ingress class (empty = cluster default)")
	deadline := fs.Duration("ready-deadline", 3*time.Minute, "a rollout without progress for this long fails")
	fs.DurationVar(&o.HealthGrace, "health-grace", 2*time.Minute, "a running pod failing its health check for this long fails the app (keep below --ready-deadline)")
	fs.StringVar(&o.HealthAddr, "health-addr", env("ODYSSEY_AGENT_HEALTH_ADDR", ":8097"), "liveness/readiness address")
	fs.StringVar(&o.MetricsAddr, "metrics-addr", "0", "metrics address (0 = off)")
	fs.BoolVar(&o.LeaderElection, "leader-elect", false, "elect a leader (run more than one replica)")
	fs.StringVar(&o.LeaderElectionNamespace, "leader-election-namespace", "", "namespace of the leader lease when out of cluster")
	secretNS := fs.String("secret-namespace", env("POD_NAMESPACE", "odyssey-system"), "namespace of the agent's Secret")
	secret := fs.String("secret", env("ODYSSEY_AGENT_SECRET", "odyssey-agent"), "the agent's Secret (enrolment token, then its identity); empty = no tunnel")
	fs.StringVar(&o.Tunnel.Dial, "gateway-dial", os.Getenv("ODYSSEY_GATEWAY_DIAL"), "host:port to reach the gateway by when not its own (an agent run outside the cluster)")
	fs.StringVar(&o.Builds.Registry, "registry", os.Getenv("ODYSSEY_REGISTRY"), "registry built images go to, as the nodes reach it (host[:port][/path]); empty = the agent does not build")
	fs.BoolVar(&o.Builds.InsecureRegistry, "registry-insecure", envBool("ODYSSEY_REGISTRY_INSECURE"), "talk to the registry over plain HTTP (a local registry)")
	fs.StringVar(&o.Builds.Platform, "build-platform", os.Getenv("ODYSSEY_BUILD_PLATFORM"), "platform images are built for (default linux/<the agent's arch>)")
	fs.StringVar(&o.Builds.AgentImage, "agent-image", os.Getenv("ODYSSEY_AGENT_IMAGE"), "the agent's own image (it runs each build's prepare step)")
	fs.StringVar(&o.Builds.SourceAddr, "source-addr", env("ODYSSEY_SOURCE_ADDR", ":8099"), "address the agent serves sources to build pods on")
	fs.StringVar(&o.Builds.SourceURL, "source-url", env("ODYSSEY_SOURCE_URL", "http://odyssey-agent-source.odyssey-system.svc:8099"), "URL build pods reach the agent's source server at")
	fs.BoolVar(&o.Access.Enabled, "access-enabled", envBool("ODYSSEY_ACCESS_ENABLED"), "the sign-in gate (odyssey-agent access) is installed: apps may require sign-in")
	o.Access.KeyNamespace, o.Access.KeyName = env("POD_NAMESPACE", "odyssey-system"), env("ODYSSEY_ACCESS_SECRET", "odyssey-access")
	if sa := os.Getenv("ODYSSEY_SERVICE_ACCOUNT"); sa != "" {
		o.ServiceAccount = client.ObjectKey{Namespace: env("POD_NAMESPACE", "odyssey-system"), Name: sa}
	}
	fs.StringVar(&o.Access.VerifyURL, "access-verify-url", env("ODYSSEY_ACCESS_VERIFY_URL", "http://odyssey-access.odyssey-system.svc:8098/verify"), "the gate's /verify URL as Traefik reaches it")
	zopts := zap.Options{}
	zopts.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	o.App.ReadyDeadline = int32(deadline.Seconds())
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zopts)))

	cfg, err := ctrl.GetConfig() // --kubeconfig, KUBECONFIG, in-cluster, ~/.kube/config
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	if *secret != "" {
		o.Secret = client.ObjectKey{Namespace: *secretNS, Name: *secret}
	}
	// In the cluster the agent has no access to Secrets but what it binds
	// for its own service account: without it, every secret would fail
	// later and opaquely. Refuse to start instead.
	if o.ServiceAccount.Name == "" && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return errors.New("ODYSSEY_SERVICE_ACCOUNT is not set: the agent's Deployment must pass its service account (fieldRef spec.serviceAccountName); see agent/install/deployment.yaml")
	}
	mgr, err := NewManager(cfg, o)
	if err != nil {
		return err
	}
	ctrl.Log.Info("odyssey-agent starting", "version", Version, "domain", o.App.Domain, "health", o.HealthAddr, "tunnel", *secret != "")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// NewManager builds the agent's controllers on cluster cfg (Start runs them).
func NewManager(cfg *rest.Config, o Options) (ctrl.Manager, error) {
	scheme, err := Scheme()
	if err != nil {
		return nil, err
	}
	// The agent caches only what it owns: its pods, Deployments, Services and
	// Ingresses carry the managed-by label; build Jobs live in their own
	// namespace. Secrets are never cached (read through the API).
	owned := cache.ByObject{Label: labels.SelectorFromSet(labels.Set{app.LabelManagedBy: app.ManagedBy})}
	byObject := map[client.Object]cache.ByObject{
		&corev1.Pod{}: owned, &appsv1.Deployment{}: owned, &corev1.Service{}: owned, &networkingv1.Ingress{}: owned,
		// Only the volumes the resource controller made (files, redis).
		&corev1.PersistentVolumeClaim{}: owned,
	}
	if o.Builds.enabled() {
		byObject[&batchv1.Job{}] = cache.ByObject{Label: owned.Label, Namespaces: map[string]cache.Config{buildctl.Namespace: {}}}
		byObject[&v1alpha1.OdysseyBuild{}] = cache.ByObject{Namespaces: map[string]cache.Config{buildctl.Namespace: {}}}
	}
	// The Middlewares are watched (a deleted one is put back) only when the
	// traefik.io API exists now: an informer on a missing kind would stop the
	// manager from starting. Without the watch, protected apps are re-checked
	// periodically (app.Reconciler).
	var mw []client.Object
	if o.Access.Ready() && hasTraefik(cfg) {
		m := app.NewMiddleware()
		byObject[m] = owned
		mw = append(mw, m)
	}
	// The same for CloudNativePG's Clusters (postgres resources): owned only
	// when the API exists now; otherwise postgres resources are re-checked
	// periodically (resource.Reconciler).
	var clusters []client.Object
	if hasAPI(cfg, resource.ClusterGVK.GroupVersion().String()) {
		c := resource.NewCluster()
		byObject[c] = owned
		clusters = append(clusters, c)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		HealthProbeBindAddress:  o.HealthAddr,
		Metrics:                 metricsserver.Options{BindAddress: o.MetricsAddr},
		LeaderElection:          o.LeaderElection,
		LeaderElectionID:        "odyssey-agent.odyssey.dev",
		LeaderElectionNamespace: o.LeaderElectionNamespace,
		Cache:                   cache.Options{ByObject: byObject},
		Client:                  client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}}},
	})
	if err != nil {
		return nil, err
	}
	rec := &app.Reconciler{Client: mgr.GetClient(), Config: o.App, HealthGrace: o.HealthGrace, Owned: mw, ExternalSecrets: mgr.GetRESTMapper()}
	if o.Access.Enabled && !o.Access.Ready() {
		ctrl.Log.Error(nil, "ODYSSEY_ACCESS_ENABLED is set but the gate configuration is not usable: protected apps will fail", "verifyURL", o.Access.VerifyURL)
	}
	if o.Access.Ready() {
		if err := mgr.Add(ensureAccessKey{cfg: cfg, o: o.Access}); err != nil {
			return nil, err
		}
		rec.Exposure = &app.Traefik{Client: mgr.GetClient(), VerifyURL: o.Access.VerifyURL, Mapper: mgr.GetRESTMapper()}
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		return nil, err
	}
	tunnelled := o.Tunnel.Store != nil || o.Secret.Name != ""
	// A deleted resource's object is removed once the control plane has been
	// told (the tunnel marks it); without a tunnel nobody needs telling.
	resources := &resource.Reconciler{Client: mgr.GetClient(), Owned: clusters, Mapper: mgr.GetRESTMapper(), AwaitReport: tunnelled}
	if err := resources.SetupWithManager(mgr); err != nil {
		return nil, err
	}
	if tunnelled {
		if err := addTunnel(cfg, mgr, o); err != nil {
			return nil, err
		}
	} else if o.Builds.enabled() {
		return nil, errors.New("building needs the tunnel (--secret)")
	}
	if o.HealthAddr != "0" {
		if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
			return nil, err
		}
		if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
			return nil, err
		}
	}
	return mgr, nil
}

// addTunnel connects the cluster to the control plane: a leader-elected
// runnable (only one replica holds the tunnel) plus the watch that reports
// status changes.
func addTunnel(cfg *rest.Config, mgr ctrl.Manager, o Options) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes discovery: %w", err)
	}
	t := o.Tunnel
	if t.Store == nil {
		t.Store = &tunnel.SecretStore{Reader: mgr.GetAPIReader(), Writer: mgr.GetClient(), Namespace: o.Secret.Namespace, Name: o.Secret.Name}
	}
	namespaces := &desired.Namespaces{Client: mgr.GetClient(), API: mgr.GetAPIReader(), Agent: o.ServiceAccount}
	syncer := &desired.Syncer{Client: mgr.GetClient(), API: mgr.GetAPIReader(), Discovery: dc, AccessGate: o.Access.Ready(), Namespaces: namespaces}
	t.Desired = syncer
	if t.Resources == nil {
		t.Resources, t.RejectedResource = syncer, desired.RejectedResourceStatus
	}
	if t.Secrets == nil {
		// Secrets are never cached: the writer reads the API.
		t.Secrets = &secretstore.Writer{Client: mgr.GetClient(), Namespaces: namespaces}
	}
	if o.ServiceAccount.Name != "" {
		if err := mgr.Add(bindEnvironment{n: namespaces}); err != nil {
			return err
		}
	}
	t.IsRejected, t.Rejected = desired.IsRejected, desired.RejectedStatus
	if t.Version == "" {
		t.Version = Version
	}
	t.Log = ctrl.Log.WithName("tunnel")
	var withTunnel func(*tunnel.Client) error
	if o.Builds.enabled() {
		if withTunnel, err = addBuilds(cfg, mgr, o, &t); err != nil {
			return err
		}
	}
	c := tunnel.New(t)
	if err := c.WatchStatuses(mgr); err != nil {
		return err
	}
	if err := c.WatchResourceStatuses(mgr); err != nil {
		return err
	}
	if withTunnel != nil {
		if err := withTunnel(c); err != nil {
			return err
		}
	}
	return mgr.Add(c)
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv(name), &n); err != nil {
		return fallback
	}
	return n
}
