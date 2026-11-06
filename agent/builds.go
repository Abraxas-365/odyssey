package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	odybuilder "github.com/Abraxas-365/odyssey/agent/builder"
	"github.com/Abraxas-365/odyssey/agent/builds"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/Abraxas-365/odyssey/agent/source"
	"github.com/Abraxas-365/odyssey/agent/tunnel"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Pinned images of build Jobs. The frontend's version is the Railpack
// library's (set with -ldflags from go.mod, like builder.RailpackVersion):
// the plan and the frontend that reads it must agree.
var (
	BuildKitImage = "moby/buildkit:v0.34.1-rootless@sha256:f68752a5a94acfcdad2d07867b7075601a421638e17fc5f28efc1c814d0c4f89"
	FrontendImage = "ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61fae6f2551103cd8b10c710cb07167dd353d561f15c69b8d7ac1c676cc"
	// ScanImage is Trivy (the multi-arch index's digest, so any node pulls its own).
	ScanImage = "aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb"
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
	// ScanImage is Trivy's image (pinned by digest); empty: no scans, and the
	// agent does not list the capability odicea.scan. Default: ScanImage.
	ScanImage string
	// NoScan turns scans off (ODYSSEY_SCAN=false): an air-gapped cluster
	// cannot download the vulnerability database.
	NoScan bool
	// SourceAddr is where the agent serves sources to build pods; SourceURL
	// is how they reach it (the odyssey-agent-source Service).
	SourceAddr string
	SourceURL  string
}

func (b BuildOptions) enabled() bool { return b.Registry != "" }

// identityVersion is the layout of BuilderIdentity's input (the prefix of
// the identity names it): change both when the list below changes.
const identityVersion = "b1"

// BuilderIdentity names everything besides the code that shapes an image
// this builder makes: the tools and their pins, the platform, where the
// image goes and the agent itself. Two builds of the same code under the
// same identity make the same app, so the control plane may reuse the first
// one's image. b must have its defaults applied.
func BuilderIdentity(b BuildOptions) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		identityVersion,
		b.BuildKitImage,
		b.Frontend,
		odybuilder.RailpackVersion,
		odybuilder.MiseVersion(),
		b.Platform,
		b.Registry,
		strconv.FormatBool(b.InsecureRegistry),
		Version,
	}, "\n")))
	return identityVersion + ":" + hex.EncodeToString(sum[:])
}

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
	if b.ScanImage == "" && !b.NoScan {
		b.ScanImage = ScanImage
	}
	if b.NoScan {
		b.ScanImage = ""
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

// capabilities adds the builder to what the cluster can do (and the scan,
// when scans run).
type capabilities struct {
	tunnel.Desired
	builder *fleetv1.Builder
	scan    bool
}

func (c capabilities) Capabilities(ctx context.Context) *fleetv1.Capabilities {
	out := c.Desired.Capabilities(ctx)
	out.Builder = c.builder
	if c.scan {
		out.Apis = append(out.Apis, CapabilityScan)
		slices.Sort(out.Apis)
	}
	return out
}

// CapabilityScan is listed among the APIs when the agent scans the images it
// builds (same string as fleet.CapabilityScan).
const CapabilityScan = "odicea.scan"

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
		Scan: buildctl.ScanConfig{Image: b.ScanImage},
	}}
	if err := rec.SetupWithManager(mgr); err != nil {
		return nil, err
	}
	// One scan Job at a time per cluster, and the databases kept warm (the
	// queue is leader-only, like the controller).
	if err := mgr.Add(&buildctl.ScanQueue{Client: mgr.GetClient(), API: mgr.GetAPIReader(), Config: rec.Config}); err != nil {
		return nil, err
	}
	m := &builds.Manager{Client: mgr.GetClient(), Registry: b.Registry,
		Logs: func(ctx context.Context, ns, pod, container string) (io.ReadCloser, error) {
			return cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, Follow: true}).Stream(ctx)
		}}
	t.Builds = m
	t.Desired = capabilities{Desired: t.Desired, builder: &fleetv1.Builder{Registry: b.Registry, Platforms: []string{b.Platform}, Identity: BuilderIdentity(b)}, scan: b.ScanImage != ""}
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
		return watchBuilds(mgr, c, b.ScanImage != "")
	}, nil
}

// watchScans reports a build's scan when its Job or pod changes.
func watchScans(mgr ctrl.Manager, c *tunnel.Client) error {
	inScans := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.GetLabels()[buildctl.LabelScan]
		return ok && o.GetNamespace() == buildctl.Namespace
	})
	toBuild := func(o client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: buildctl.Namespace, Name: o.GetLabels()[buildctl.LabelScan]}}}
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("odyssey-scan-report").
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			return toBuild(o)
		}), builder.WithPredicates(inScans)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			return toBuild(o)
		}), builder.WithPredicates(inScans)).
		// a scan given up before it started is recorded on its build
		Watches(&v1alpha1.OdysseyBuild{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
		}), builder.WithPredicates(predicate.Funcs{
			CreateFunc:  func(event.CreateEvent) bool { return false },
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetAnnotations()[buildctl.AnnotationScanError] != e.ObjectNew.GetAnnotations()[buildctl.AnnotationScanError]
			},
		})).
		Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
			c.ScanChanged(req.Name)
			return reconcile.Result{}, nil
		}))
}

// watchBuilds reports a build when its status changes.
func watchBuilds(mgr ctrl.Manager, c *tunnel.Client, scans bool) error {
	if scans {
		if err := watchScans(mgr, c); err != nil {
			return err
		}
	}
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
