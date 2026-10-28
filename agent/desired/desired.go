// Package desired is the agent's side of the desired state: it turns the
// apps the control plane sends (proto/fleet/v1) into OdysseyApps, reads back
// what the app controller observed, and describes what the cluster offers.
// It never talks to the gateway itself (agent/tunnel does).
package desired

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=create
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyapps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingressclasses,verbs=list
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=list

// Labels the agent puts on what it creates from the desired state.
const (
	LabelManagedBy    = "app.kubernetes.io/managed-by"
	ManagedBy         = "odyssey"
	LabelOrganization = "odyssey.dev/organization"
	LabelProject      = "odyssey.dev/project"
	LabelEnvironment  = "odyssey.dev/environment"
)

// Limits of what is reported (the gateway refuses more: fleet.MaxMessage).
const (
	maxMessage = 2000
	maxURL     = 2048
)

// Namespace is where an environment's apps run: one namespace per
// environment (environment IDs are unique across organizations).
func Namespace(environment string) string { return "odyssey-" + environment }

// Syncer applies desired apps and observes them.
type Syncer struct {
	// Client writes OdysseyApps and namespaces and reads OdysseyApps (the
	// manager's cached client is fine: the app controller caches them).
	Client client.Client
	// API lists cluster-scoped classes without caching them (nil: Client).
	API client.Reader
	// Discovery reads the Kubernetes version and API groups (nil: none).
	Discovery discovery.DiscoveryInterface
}

// Rejected is what retrying cannot fix: the cluster refused the app. It is
// reported to the control plane as a failed app (reason config).
type Rejected struct{ Message string }

func (r *Rejected) Error() string { return r.Message }

// IsRejected reports whether err is a Rejected.
func IsRejected(err error) bool {
	var r *Rejected
	return errors.As(err, &r)
}

// Valid checks what the agent needs of a desired app before touching the
// cluster: an app it cannot name or report is dropped (the gateway sent
// garbage, there is nobody to tell).
func Valid(a *fleetv1.App) error {
	switch {
	case a == nil:
		return errors.New("no app")
	case !isUUID(a.GetEnvironmentId()) || !isUUID(a.GetOrganizationId()) || !isUUID(a.GetProjectId()):
		return errors.New("app needs organization, project and environment ids")
	case len(validation.IsDNS1123Label(a.GetName())) > 0:
		return fmt.Errorf("app name %q is not a DNS label", a.GetName())
	case a.GetRevision() < 1:
		return errors.New("app revision must be positive")
	}
	return nil
}

func isUUID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u != uuid.Nil && u.String() == s
}

// Apply writes the desired app unless the cluster already has the same or a
// newer revision. A conflicting concurrent write is returned as a plain
// error (the caller retries and re-reads); what retrying cannot fix is a
// *Rejected: a spec the API refuses, a cluster without the OdysseyApp CRD,
// or permissions the agent lacks.
func (s *Syncer) Apply(ctx context.Context, a *fleetv1.App) error {
	if err := Valid(a); err != nil {
		return &Rejected{Message: err.Error()}
	}
	if err := s.ensureNamespace(ctx, a); err != nil {
		return permanent(err)
	}
	labels := make(map[string]string, len(a.GetLabels())+3)
	for k, v := range a.GetLabels() {
		labels[k] = v
	}
	// The agent's own labels win: reports are keyed by them.
	labels[LabelOrganization], labels[LabelProject], labels[LabelEnvironment] = a.GetOrganizationId(), a.GetProjectId(), a.GetEnvironmentId()
	spec := v1alpha1.OdysseyAppSpec{
		Revision: a.GetRevision(), Image: a.GetImage(), Port: a.GetPort(), HealthPath: a.GetHealthPath(),
		Replicas: a.GetReplicas(), Env: a.GetEnv(), Labels: a.GetLabels(),
	}
	ns := Namespace(a.GetEnvironmentId())
	var cur v1alpha1.OdysseyApp
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: a.GetName()}, &cur)
	switch {
	case apierrors.IsNotFound(err):
		obj := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Name: a.GetName(), Namespace: ns, Labels: labels}, Spec: spec}
		err = s.Client.Create(ctx, obj)
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("apply %s: created concurrently, retrying", a.GetName())
		}
	case err != nil:
		return permanent(fmt.Errorf("read %s: %w", a.GetName(), err))
	case cur.Spec.Revision >= a.GetRevision():
		return nil // the same or a newer revision already got here
	default:
		cur.Spec, cur.Labels = spec, labels
		err = s.Client.Update(ctx, &cur) // resourceVersion guards against a concurrent writer
	}
	return permanent(err)
}

func permanent(err error) error {
	switch {
	case err == nil:
		return nil
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return &Rejected{Message: "the cluster rejected the app: " + err.Error()}
	case meta.IsNoMatchError(err):
		return &Rejected{Message: "the OdysseyApp resource is not installed in the cluster: " + err.Error()}
	case apierrors.IsForbidden(err):
		return &Rejected{Message: "the agent may not write there: " + err.Error()}
	}
	return err
}

func (s *Syncer) ensureNamespace(ctx context.Context, a *fleetv1.App) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(a.GetEnvironmentId()), Labels: map[string]string{
		LabelManagedBy:    ManagedBy,
		LabelOrganization: a.GetOrganizationId(),
		LabelProject:      a.GetProjectId(),
		LabelEnvironment:  a.GetEnvironmentId(),
		// Apps run under the baseline Pod Security profile; restricted
		// (non-root) arrives with per-organization policy.
		"pod-security.kubernetes.io/enforce": "baseline",
	}}}
	if err := s.Client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("namespace %s: %w", ns.Name, err)
	}
	return nil
}

// RejectedStatus is how a rejected app is reported.
func RejectedStatus(a *fleetv1.App, err error) *fleetv1.AppStatus {
	return &fleetv1.AppStatus{
		EnvironmentId: a.GetEnvironmentId(), Name: a.GetName(), ObservedRevision: a.GetRevision(),
		Phase: string(v1alpha1.PhaseFailed), Reason: string(v1alpha1.ReasonConfig), Message: clip(err.Error(), maxMessage),
	}
}

// Statuses lists what the app controller observed of every app the agent
// applied (apps it has not acted on yet are left out).
func (s *Syncer) Statuses(ctx context.Context) ([]*fleetv1.AppStatus, error) {
	var list v1alpha1.OdysseyAppList
	if err := s.Client.List(ctx, &list, client.HasLabels{LabelEnvironment}); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	out := make([]*fleetv1.AppStatus, 0, len(list.Items))
	for i := range list.Items {
		if st := statusOf(&list.Items[i]); st != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

// Status is one app's (nil when it does not exist or was not acted on yet).
func (s *Syncer) Status(ctx context.Context, environment, name string) (*fleetv1.AppStatus, error) {
	var a v1alpha1.OdysseyApp
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: Namespace(environment), Name: name}, &a); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("status of %s: %w", name, err)
	}
	return statusOf(&a), nil
}

func statusOf(a *v1alpha1.OdysseyApp) *fleetv1.AppStatus {
	env := a.Labels[LabelEnvironment]
	st := a.Status
	if !isUUID(env) || a.Namespace != Namespace(env) || st.ObservedRevision < 1 {
		return nil
	}
	switch st.Phase {
	case v1alpha1.PhaseProgressing, v1alpha1.PhaseReady, v1alpha1.PhaseFailed:
	default:
		return nil
	}
	return &fleetv1.AppStatus{
		EnvironmentId: env, Name: a.Name, ObservedRevision: st.ObservedRevision, Phase: string(st.Phase),
		Reason: string(st.Reason), Message: clip(st.Message, maxMessage), ReadyReplicas: st.ReadyReplicas,
		Url: clip(st.URL, maxURL),
	}
}

// clip shortens s to n runes and drops control characters other than tab
// and newline (the gateway refuses them).
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Platform APIs the control plane may use when present.
var knownAPIs = []string{
	"postgresql.cnpg.io", "kyverno.io", "keda.sh", "gateway.networking.k8s.io",
	"cert-manager.io", "external-secrets.io",
}

// Capabilities describes the cluster: its Kubernetes version, which known
// platform APIs it has, and its ingress and storage classes. Whatever
// cannot be read is left empty (the agent still connects).
func (s *Syncer) Capabilities(ctx context.Context) *fleetv1.Capabilities {
	out := &fleetv1.Capabilities{Apis: []string{}, IngressClasses: []string{}, StorageClasses: []string{}}
	if s.Discovery != nil {
		if v, err := s.Discovery.ServerVersion(); err == nil {
			out.KubernetesVersion = v.GitVersion
		}
		if groups, err := s.Discovery.ServerGroups(); err == nil {
			for _, g := range groups.Groups {
				if slices.Contains(knownAPIs, g.Name) {
					out.Apis = append(out.Apis, g.Name)
				}
			}
			slices.Sort(out.Apis)
		}
	}
	reader := s.API
	if reader == nil {
		reader = s.Client
	}
	var ic networkingv1.IngressClassList
	if err := reader.List(ctx, &ic); err == nil {
		for _, c := range ic.Items {
			name := c.Name
			if c.Annotations[networkingv1.AnnotationIsDefaultIngressClass] == "true" {
				name += " (default)"
			}
			out.IngressClasses = append(out.IngressClasses, name)
		}
	}
	var sc storagev1.StorageClassList
	if err := reader.List(ctx, &sc); err == nil {
		for _, c := range sc.Items {
			name := c.Name
			if c.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
				name += " (default)"
			}
			out.StorageClasses = append(out.StorageClasses, name)
		}
	}
	slices.Sort(out.IngressClasses)
	slices.Sort(out.StorageClasses)
	return out
}
