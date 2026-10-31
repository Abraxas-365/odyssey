package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/builds"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/Abraxas-365/odyssey/agent/source"
	"github.com/Abraxas-365/odyssey/agent/tunnel"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Pinned images of build Jobs. The frontend's version is the Railpack
// library's (set with -ldflags from go.mod, like builder.RailpackVersion):
// the plan and the frontend that reads it must agree.
var (
	BuildKitImage = "moby/buildkit:v0.34.1-rootless@sha256:f68752a5a94acfcdad2d07867b7075601a421638e17fc5f28efc1c814d0c4f89"
	FrontendImage = "ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61fae6f2551103cd8b10c710cb07167dd353d561f15c69b8d7ac1c676cc"
)

// BuildOptions turn the agent into its cluster's builder.
type BuildOptions struct {
	// Registry is where images are pushed and pulled from (host[:port][/path]);
	// empty: the agent does not build.
	Registry string
	// InsecureRegistry talks to it over plain HTTP (a local registry).
	InsecureRegistry bool
	// Platform built for (default: the agent's own, linux/<arch>).
	Platform string
	// AgentImage runs a build's prepare step (default: ODYSSEY_AGENT_IMAGE).
	AgentImage    string
	BuildKitImage string
	Frontend      string
	// SourceAddr is where the agent serves sources to build pods; SourceURL
	// is how they reach it (the odyssey-agent-source Service).
	SourceAddr string
	SourceURL  string
}

func (b BuildOptions) enabled() bool { return b.Registry != "" }

func (b *BuildOptions) defaults() {
	if b.Platform == "" {
		b.Platform = "linux/" + runtime.GOARCH
	}
	if b.BuildKitImage == "" {
		b.BuildKitImage = BuildKitImage
	}
	if b.Frontend == "" {
		b.Frontend = FrontendImage
	}
	if b.SourceAddr == "" {
		b.SourceAddr = ":8099"
	}
}

func (b BuildOptions) check() error {
	switch {
	case b.AgentImage == "":
		return errors.New("building needs the agent's image (--agent-image or ODYSSEY_AGENT_IMAGE)")
	case b.SourceURL == "":
		return errors.New("building needs the URL build pods reach the agent at (--source-url)")
	}
	return nil
}

// capabilities adds the builder to what the cluster can do.
type capabilities struct {
	tunnel.Desired
	builder *fleetv1.Builder
}

func (c capabilities) Capabilities(ctx context.Context) *fleetv1.Capabilities {
	out := c.Desired.Capabilities(ctx)
	out.Builder = c.builder
	return out
}

// addBuilds runs builds: the controller (every replica, leader-elected by
// the manager), the source server (every replica; only the leader's tunnel
// can fetch, the pod retries) and the tunnel's side.
func addBuilds(cfg *rest.Config, mgr ctrl.Manager, o Options, t *tunnel.Options) (func(*tunnel.Client) error, error) {
	b := o.Builds
	b.defaults()
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	if b.AgentImage == "" {
		if b.AgentImage, err = ownImage(cs, o.Secret.Namespace); err != nil {
			return nil, err
		}
	}
	if err := b.check(); err != nil {
		return nil, err
	}
	rec := &buildctl.Reconciler{Client: mgr.GetClient(), Config: buildctl.Config{
		AgentImage: b.AgentImage, BuildKitImage: b.BuildKitImage, Frontend: b.Frontend, SourceURL: b.SourceURL,
		InsecureRegistry: b.InsecureRegistry, Platform: b.Platform,
	}}
	if err := rec.SetupWithManager(mgr); err != nil {
		return nil, err
	}
	m := &builds.Manager{Client: mgr.GetClient(), Registry: b.Registry,
		Logs: func(ctx context.Context, ns, pod, container string) (io.ReadCloser, error) {
			return cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, Follow: true}).Stream(ctx)
		}}
	t.Builds = m
	t.Desired = capabilities{Desired: t.Desired, builder: &fleetv1.Builder{Registry: b.Registry, Platforms: []string{b.Platform}}}
	return func(c *tunnel.Client) error {
		srv := source.New(mgr.GetAPIReader(), func(ctx context.Context, id string) (io.ReadCloser, error) {
			r, err := c.FetchSource(ctx, id)
			switch {
			case errors.Is(err, tunnel.ErrSourceNotFound):
				return nil, source.ErrNotFound
			case errors.Is(err, tunnel.ErrSourceBusy):
				return nil, source.ErrBusy
			case errors.Is(err, tunnel.ErrNoTunnel):
				return nil, source.ErrUnavailable
			}
			return r, err
		}, ctrl.Log.WithName("source"))
		if err := mgr.Add(source.Runnable{Addr: b.SourceAddr, Server: srv}); err != nil {
			return err
		}
		return watchBuilds(mgr, c)
	}, nil
}

// watchBuilds reports a build when its status changes.
func watchBuilds(mgr ctrl.Manager, c *tunnel.Client) error {
	changed := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return !equality.Semantic.DeepEqual(e.ObjectOld.(*v1alpha1.OdysseyBuild).Status, e.ObjectNew.(*v1alpha1.OdysseyBuild).Status)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("odysseybuild-report").
		For(&v1alpha1.OdysseyBuild{}, builder.WithPredicates(changed)).
		Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
			if req.Namespace == buildctl.Namespace {
				c.BuildChanged(req.Name)
			}
			return reconcile.Result{}, nil
		}))
}

// +kubebuilder:rbac:groups="",namespace=odyssey-system,resources=pods,verbs=get

// ownImage is the image of the agent's own pod (POD_NAME, in namespace):
// build pods run it for their prepare step. Out of the cluster there is
// none (--agent-image).
func ownImage(cs kubernetes.Interface, namespace string) (string, error) {
	name := os.Getenv("POD_NAME")
	if name == "" || namespace == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read the agent's own pod for its image: %w", err)
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == "agent" {
			return c.Image, nil
		}
	}
	return "", errors.New("the agent's pod has no container named agent")
}

func envBool(name string) bool {
	v := os.Getenv(name)
	return v == "1" || v == "true"
}
