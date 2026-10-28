// Package releasekube is the release.Cluster adapter that talks to a
// Kubernetes API directly: it writes OdysseyApp resources, which the
// odyssey-agent in that cluster reconciles, and reads back their status.
//
// Phase 1a-a2 uses it with one cluster reachable from the worker (a kubeconfig
// or in-cluster). Phase 1a-a3 replaces it, for enrolled clusters, with the
// fleet adapter that sends the same desired state through the agent's tunnel.
package releasekube

import (
	"context"
	"fmt"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/internal/release"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Labels of the namespaces the adapter creates.
const (
	labelManagedBy    = "app.kubernetes.io/managed-by"
	managedBy         = "odyssey"
	labelOrganization = "odyssey.dev/organization"
	labelProject      = "odyssey.dev/project"
	labelEnvironment  = "odyssey.dev/environment"
)

// Cluster implements release.Cluster on a Kubernetes API.
type Cluster struct {
	c client.Client
}

var _ release.Cluster = (*Cluster)(nil)

// New uses an existing client (tests pass a fake one). Its scheme must know
// core types and v1alpha1 (Scheme).
func New(c client.Client) *Cluster { return &Cluster{c: c} }

// Scheme knows the types the adapter reads and writes.
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

// Connect builds a Cluster from a kubeconfig file ("" = in-cluster).
func Connect(kubeconfig string) (*Cluster, error) {
	var (
		cfg *rest.Config
		err error
	)
	if kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	s, err := Scheme()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return New(c), nil
}

// Namespace is where an environment's apps run: one namespace per
// environment (environment IDs are unique across organizations).
func Namespace(t release.Target) string { return "odyssey-" + t.Environment.String() }

// Apply writes the desired app unless the cluster already has the same or a
// newer revision. A conflicting concurrent write is returned as a plain
// error (the workflow retries and re-reads). What retrying cannot fix fails
// the deployment with APPLY_FAILED: a spec the API rejects, a cluster
// without the agent's CRD, or credentials that may not write there.
func (k *Cluster) Apply(ctx context.Context, t release.Target, app release.DesiredApp) error {
	if err := k.ensureNamespace(ctx, t); err != nil {
		return permanent(err)
	}
	spec := v1alpha1.OdysseyAppSpec{
		Revision: app.Revision, Image: app.Image, Port: int32(app.Port), HealthPath: app.HealthPath,
		Replicas: int32(app.Replicas), Env: app.Env, Labels: app.Labels,
	}
	var cur v1alpha1.OdysseyApp
	err := k.c.Get(ctx, client.ObjectKey{Namespace: Namespace(t), Name: app.Name}, &cur)
	switch {
	case apierrors.IsNotFound(err):
		obj := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: Namespace(t), Labels: app.Labels}, Spec: spec}
		err = k.c.Create(ctx, obj)
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("apply %s: created concurrently, retrying", app.Name)
		}
	case err != nil:
		return permanent(fmt.Errorf("read %s: %w", app.Name, err))
	case cur.Spec.Revision >= app.Revision:
		return nil // the same or a newer deployment already got there
	default:
		cur.Spec, cur.Labels = spec, app.Labels
		err = k.c.Update(ctx, &cur) // resourceVersion guards against a concurrent writer
	}
	return permanent(err)
}

// permanent turns what retrying cannot fix into APPLY_FAILED.
func permanent(err error) error {
	switch {
	case err == nil:
		return nil
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return release.Failed(release.CodeApplyFailed, "El clúster rechazó la configuración de la app. "+err.Error())
	case meta.IsNoMatchError(err):
		return release.Failed(release.CodeApplyFailed, "El agente de Odyssey no está instalado en el clúster (falta el recurso OdysseyApp). "+err.Error())
	case apierrors.IsForbidden(err):
		return release.Failed(release.CodeApplyFailed, "Odyssey no tiene permiso para desplegar en el clúster. "+err.Error())
	}
	return err
}

// Status reads what the agent observed (ObservedRevision 0 when the app does
// not exist or the agent has not looked at it yet).
func (k *Cluster) Status(ctx context.Context, t release.Target, name string) (release.AppStatus, error) {
	var a v1alpha1.OdysseyApp
	if err := k.c.Get(ctx, client.ObjectKey{Namespace: Namespace(t), Name: name}, &a); err != nil {
		if apierrors.IsNotFound(err) {
			return release.AppStatus{}, nil
		}
		return release.AppStatus{}, fmt.Errorf("status of %s: %w", name, err)
	}
	s := a.Status
	out := release.AppStatus{
		ObservedRevision: s.ObservedRevision, Message: s.Message, ReadyReplicas: int(s.ReadyReplicas), URL: s.URL,
		Phase: release.PhaseProgressing,
	}
	switch s.Phase {
	case v1alpha1.PhaseReady:
		out.Phase = release.PhaseReady
	case v1alpha1.PhaseFailed:
		out.Phase = release.PhaseFailed
		out.Reason = map[v1alpha1.Reason]release.FailureReason{
			v1alpha1.ReasonImagePull: release.FailureImagePull,
			v1alpha1.ReasonCrash:     release.FailureCrash,
			v1alpha1.ReasonConfig:    release.FailureConfig,
		}[s.Reason]
		if out.Reason == "" {
			out.Reason = release.FailureUnhealthy
		}
	}
	return out, nil
}

// ensureNamespace creates the environment's namespace on first use.
func (k *Cluster) ensureNamespace(ctx context.Context, t release.Target) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(t), Labels: map[string]string{
		labelManagedBy:    managedBy,
		labelOrganization: t.Organization.String(),
		labelProject:      t.Project.String(),
		labelEnvironment:  t.Environment.String(),
		// Apps run under the baseline Pod Security profile; restricted
		// (non-root) arrives with per-organization policy.
		"pod-security.kubernetes.io/enforce": "baseline",
	}}}
	if err := k.c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("namespace %s: %w", ns.Name, err)
	}
	return nil
}
