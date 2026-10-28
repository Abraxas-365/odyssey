// Package agent is Odyssey's data plane: the operator that runs in each
// cluster and reconciles the desired state the control plane sends (today
// OdysseyApps). It never imports the control plane (internal/): the two share
// only agent/api (architecture test).
package agent

import (
	"flag"
	"fmt"
	"os"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/app"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
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
}

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
	mgr, err := NewManager(cfg, o)
	if err != nil {
		return err
	}
	ctrl.Log.Info("odyssey-agent starting", "domain", o.App.Domain, "health", o.HealthAddr)
	return mgr.Start(ctrl.SetupSignalHandler())
}

// NewManager builds the agent's controllers on cluster cfg (Start runs them).
func NewManager(cfg *rest.Config, o Options) (ctrl.Manager, error) {
	scheme, err := Scheme()
	if err != nil {
		return nil, err
	}
	// The agent caches only what it owns: its pods, Deployments, Services and
	// Ingresses carry the managed-by label.
	owned := cache.ByObject{Label: labels.SelectorFromSet(labels.Set{app.LabelManagedBy: app.ManagedBy})}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		HealthProbeBindAddress:  o.HealthAddr,
		Metrics:                 metricsserver.Options{BindAddress: o.MetricsAddr},
		LeaderElection:          o.LeaderElection,
		LeaderElectionID:        "odyssey-agent.odyssey.dev",
		LeaderElectionNamespace: o.LeaderElectionNamespace,
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: owned, &appsv1.Deployment{}: owned, &corev1.Service{}: owned, &networkingv1.Ingress{}: owned,
		}},
	})
	if err != nil {
		return nil, err
	}
	if err := (&app.Reconciler{Client: mgr.GetClient(), Config: o.App, HealthGrace: o.HealthGrace}).SetupWithManager(mgr); err != nil {
		return nil, err
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
