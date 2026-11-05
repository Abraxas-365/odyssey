// Package desired is the agent's side of the desired state: it turns the
// apps the control plane sends (proto/fleet/v1) into OdysseyApps, reads back
// what the app controller observed, and describes what the cluster offers.
// It never talks to the gateway itself (agent/tunnel does).
package desired

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=odyssey-agent-environment,verbs=bind
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyapps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=odyssey.dev,resources=odysseyresources,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingressclasses,verbs=list
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=list
// +kubebuilder:rbac:groups=external-secrets.io,resources=clustersecretstores,verbs=list

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
	// AccessGate says the agent's install has the sign-in gate configured
	// (ODYSSEY_ACCESS_ENABLED): see CapabilityAccess.
	AccessGate bool
	// Namespaces creates the environments' namespaces (nil: on Client, binding
	// nothing).
	Namespaces *Namespaces
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
	if err := validSecretRefs(a.GetSecrets()); err != nil {
		return err
	}
	return validResourceRefs(a.GetResources())
}

// MaxSecrets bounds an app's secret variables.
const MaxSecrets = 100

// maxRef bounds the free text of an external reference.
const maxRef = 1024

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidEnvName reports whether s can be the name of an environment variable
// (what a secret is called).
func ValidEnvName(s string) bool { return envName.MatchString(s) }

func validSecretRefs(refs []*fleetv1.SecretRef) error {
	if len(refs) > MaxSecrets {
		return fmt.Errorf("app has %d secrets, at most %d", len(refs), MaxSecrets)
	}
	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		name := r.GetName()
		switch {
		case !ValidEnvName(name):
			return fmt.Errorf("secret name %q is not a valid environment variable name", name)
		case seen[name]:
			return fmt.Errorf("secret %q is repeated", name)
		}
		seen[name] = true
		switch v1alpha1.SecretSource(r.GetSource()) {
		case v1alpha1.SecretManaged:
			if r.GetVersion() < 1 {
				return fmt.Errorf("managed secret %q needs a version of at least 1", name)
			}
		case v1alpha1.SecretExternal:
			if r.GetStore() == "" || r.GetKey() == "" {
				return fmt.Errorf("external secret %q needs a store and a key", name)
			}
			if len(validation.IsDNS1123Subdomain(r.GetStore())) > 0 {
				return fmt.Errorf("external secret %q: store %q is not a valid name", name, r.GetStore())
			}
			if len(r.GetKey()) > maxRef || len(r.GetProperty()) > maxRef {
				return fmt.Errorf("external secret %q: key or property too long", name)
			}
		default:
			return fmt.Errorf("secret %q has source %q, not managed or external", name, r.GetSource())
		}
	}
	return nil
}

// ValidSecret checks a delivered secret before anything is opened or written.
func ValidSecret(s *fleetv1.Secret) error {
	switch {
	case s == nil:
		return errors.New("no secret")
	case !isUUID(s.GetEnvironmentId()) || !isUUID(s.GetOrganizationId()) || !isUUID(s.GetProjectId()):
		return errors.New("secret needs organization, project and environment ids")
	case len(validation.IsDNS1123Label(s.GetApp())) > 0:
		return fmt.Errorf("app name %q is not a DNS label", s.GetApp())
	case !ValidEnvName(s.GetName()):
		return fmt.Errorf("secret name %q is not a valid environment variable name", s.GetName())
	case s.GetVersion() < 1:
		return errors.New("secret version must be positive")
	case len(s.GetSealed()) == 0:
		return errors.New("secret has no value")
	}
	return nil
}

func isUUID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u != uuid.Nil && u.String() == s
}

// secretsOf maps the proto's secret references (sorted by name, so the same
// set always makes the same spec).
func secretsOf(refs []*fleetv1.SecretRef) []v1alpha1.SecretRefSpec {
	if len(refs) == 0 {
		return nil
	}
	out := make([]v1alpha1.SecretRefSpec, 0, len(refs))
	for _, r := range refs {
		s := v1alpha1.SecretRefSpec{Name: r.GetName(), Source: v1alpha1.SecretSource(r.GetSource())}
		if s.Source == v1alpha1.SecretManaged {
			s.Version = r.GetVersion()
		} else {
			s.Store, s.Key, s.Property = r.GetStore(), r.GetKey(), r.GetProperty()
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b v1alpha1.SecretRefSpec) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Apply writes the desired app unless the cluster already has a newer
// revision, or the same revision with the same spec. A conflicting concurrent write is returned as a plain
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
	access, err := accessOf(a)
	if err != nil {
		return &Rejected{Message: err.Error()}
	}
	labels := make(map[string]string, len(a.GetLabels())+3)
	for k, v := range a.GetLabels() {
		labels[k] = v
	}
	// The agent's own labels win: reports are keyed by them.
	labels[LabelOrganization], labels[LabelProject], labels[LabelEnvironment] = a.GetOrganizationId(), a.GetProjectId(), a.GetEnvironmentId()
	spec := v1alpha1.OdysseyAppSpec{
		Revision: a.GetRevision(), Image: a.GetImage(), Port: a.GetPort(), HealthPath: a.GetHealthPath(),
		Replicas: a.GetReplicas(), Env: a.GetEnv(), Labels: a.GetLabels(), Access: access, Secrets: secretsOf(a.GetSecrets()),
		Resources: resourcesOf(a.GetResources()), AI: aiOf(a.GetAi()),
	}
	ns := Namespace(a.GetEnvironmentId())
	var cur v1alpha1.OdysseyApp
	err = s.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: a.GetName()}, &cur)
	switch {
	case apierrors.IsNotFound(err):
		obj := &v1alpha1.OdysseyApp{ObjectMeta: metav1.ObjectMeta{Name: a.GetName(), Namespace: ns, Labels: labels}, Spec: spec}
		err = s.Client.Create(ctx, obj)
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("apply %s: created concurrently, retrying", a.GetName())
		}
		if err == nil {
			return s.fenceAI(ctx, ns, spec.AI != nil)
		}
	case err != nil:
		return permanent(fmt.Errorf("read %s: %w", a.GetName(), err))
	case cur.Spec.Revision > a.GetRevision():
		return nil // a newer revision already got here
	case cur.Spec.Revision == a.GetRevision() && equality.Semantic.DeepEqual(cur.Spec, spec):
		// Nothing to write. A fence that failed last time (the namespace's
		// AI policy) is tried again: the retry of a failed Apply lands here.
		return s.fenceAI(ctx, ns, spec.AI != nil)
	default:
		// A higher revision, or the same one whose spec differs (the control
		// plane re-sent it, e.g. with another access mode): the desired
		// state wins. The status is not reset: the agent re-observes it
		// against the new spec on the next pass.
		hadAI := cur.Spec.AI != nil
		cur.Spec, cur.Labels = spec, labels
		if err = s.Client.Update(ctx, &cur); err != nil { // resourceVersion guards against a concurrent writer
			return permanent(err)
		}
		return s.fenceAI(ctx, ns, hadAI || spec.AI != nil)
	}
	return permanent(err)
}

// accessOf maps the proto's access to the CRD's. An empty mode is "public":
// it only comes from revisions created before the sign-in gate existed (the
// control plane always sends an explicit mode now).
func accessOf(a *fleetv1.App) (*v1alpha1.AccessSpec, error) {
	mode := v1alpha1.AccessMode(a.GetAccess().GetMode())
	switch mode {
	case "":
		mode = v1alpha1.AccessPublic
	case v1alpha1.AccessPublic, v1alpha1.AccessTeam, v1alpha1.AccessOrganization:
	default:
		return nil, fmt.Errorf("app access %q is not public, team or organization", mode)
	}
	return &v1alpha1.AccessSpec{Mode: mode, Organization: a.GetOrganizationId()}, nil
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
	n := s.Namespaces
	if n == nil {
		n = &Namespaces{Client: s.Client}
	}
	return n.Ensure(ctx, a.GetOrganizationId(), a.GetProjectId(), a.GetEnvironmentId())
}

// EnvironmentRole is the ClusterRole (agent/install/rbac.yaml) with the
// agent's destructive and secret-bearing verbs: apps' Secrets, and creating,
// resizing and deleting the volumes, databases and caches of resources. It
// is never bound cluster-wide: the agent binds it, as a RoleBinding of the
// same name, only in the namespaces it creates for environments (and the
// install's admission policy refuses a binding anywhere else), so it can
// neither read the Secrets of the rest of the cluster nor erase its data.
const EnvironmentRole = "odyssey-agent-environment"

// Namespaces creates the environments' namespaces and gives the agent the
// EnvironmentRole in them.
type Namespaces struct {
	Client client.Client
	// API reads namespaces without caching them (nil: Client).
	API client.Reader
	// Agent is the agent's service account. Empty (an agent run outside the
	// cluster on its own credentials) binds nothing.
	Agent types.NamespacedName
	// CanUseRole says whether the agent may already use the EnvironmentRole
	// in a namespace (nil: a SelfSubjectAccessReview of creating Secrets,
	// one of the verbs the role grants). A new binding takes a moment to
	// apply; Ensure waits for it.
	CanUseRole func(ctx context.Context, namespace string) (bool, error)
	// Fence writes the egress NetworkPolicies of a namespace once it is
	// bound (agent/egress.Enforcer; nil: none).
	Fence Fencer
}

// Fencer fences a namespace with the cluster's egress policy.
type Fencer interface {
	Fence(ctx context.Context, namespace string) error
}

// bindWait bounds how long Ensure waits for a new binding to apply.
var bindWait = wait.Backoff{Duration: 100 * time.Millisecond, Factor: 1.5, Steps: 12, Cap: 2 * time.Second}

// Ensure creates the environment's namespace when it is not there (the
// first app or the first secret of an environment makes it) and binds
// EnvironmentRole in it. A namespace of that name the agent does not manage is
// refused (Rejected): the agent never takes over someone else's namespace.
func (n *Namespaces) Ensure(ctx context.Context, organization, project, environment string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(environment), Labels: map[string]string{
		LabelManagedBy:    ManagedBy,
		LabelOrganization: organization,
		LabelProject:      project,
		LabelEnvironment:  environment,
		// Apps run under the baseline Pod Security profile; restricted
		// (non-root) arrives with per-organization policy.
		"pod-security.kubernetes.io/enforce": "baseline",
	}}}
	err := n.Client.Create(ctx, ns)
	switch {
	case err == nil:
	case apierrors.IsAlreadyExists(err):
		var cur corev1.Namespace
		if err := n.reader().Get(ctx, client.ObjectKey{Name: ns.Name}, &cur); err != nil {
			return fmt.Errorf("namespace %s: %w", ns.Name, err)
		}
		if cur.Labels[LabelManagedBy] != ManagedBy {
			return &Rejected{Message: fmt.Sprintf("the namespace %s already exists and is not managed by Odyssey", ns.Name)}
		}
	default:
		return fmt.Errorf("namespace %s: %w", ns.Name, err)
	}
	if err := n.bind(ctx, ns.Name, true); err != nil {
		return err
	}
	if n.Fence != nil {
		// Before the first pod: the namespace never runs unfenced.
		return n.Fence.Fence(ctx, ns.Name)
	}
	return nil
}

// BindAll binds EnvironmentRole in every namespace the agent created (an
// agent upgraded from one that bound an older role, or a binding someone
// deleted).
func (n *Namespaces) BindAll(ctx context.Context) error {
	var list corev1.NamespaceList
	if err := n.reader().List(ctx, &list, client.MatchingLabels{LabelManagedBy: ManagedBy}); err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}
	var errs []error
	for _, ns := range list.Items {
		if strings.HasPrefix(ns.Name, Namespace("")) && ns.DeletionTimestamp == nil {
			// No wait here: a binding that has not applied yet is retried by
			// the reconcilers, and an upgrade with many namespaces must not
			// wait on each in turn.
			errs = append(errs, n.bind(ctx, ns.Name, false))
		}
	}
	return errors.Join(errs...)
}

// bind creates the binding in namespace; with settle, it returns only once the
// agent may use the role there (also when the binding was already there: a
// concurrent Ensure may have just made it).
func (n *Namespaces) bind(ctx context.Context, namespace string, settle bool) error {
	if n.Agent.Name == "" {
		return nil
	}
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: EnvironmentRole, Namespace: namespace, Labels: map[string]string{LabelManagedBy: ManagedBy}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: EnvironmentRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: n.Agent.Name, Namespace: n.Agent.Namespace}},
	}
	if err := n.Client.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("let the agent manage environment %s: %w", namespace, err)
	}
	if !settle {
		return nil
	}
	can := n.CanUseRole
	if can == nil {
		can = n.selfReview
	}
	var last error
	if err := wait.ExponentialBackoffWithContext(ctx, bindWait, func(ctx context.Context) (bool, error) {
		ok, err := can(ctx, namespace)
		last = err
		return ok, nil
	}); err != nil {
		return fmt.Errorf("the agent's access to %s did not apply: %w", namespace, errors.Join(err, last))
	}
	return nil
}

func (n *Namespaces) selfReview(ctx context.Context, namespace string) (bool, error) {
	r := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: "create", Resource: "secrets"},
	}}
	if err := n.Client.Create(ctx, r); err != nil {
		return false, err
	}
	return r.Status.Allowed, nil
}

func (n *Namespaces) reader() client.Reader {
	if n.API != nil {
		return n.API
	}
	return n.Client
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
	if st.ObservedGeneration != a.Generation {
		return nil // the spec changed after this status (same revision): not fresh yet
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
	"cert-manager.io", "external-secrets.io", "traefik.io",
}

// CapabilityAccess is reported among the APIs when the cluster can protect
// apps with the sign-in gate: the gate is configured (Syncer.AccessGate) and
// the ingress's API group exists (traefik.io today).
const CapabilityAccess = "odicea.access"

// CapabilityAI is always reported: wiring an app to the AI gateway is the
// agent's own work (variables and a reference to the key in <app>-secrets),
// it needs nothing from the cluster. Same string as fleet.CapabilityAI; an
// agent older than the AI does not list it, and the control plane then refuses
// to turn the AI on for that cluster.
const CapabilityAI = "odicea.ai"

// Capabilities describes the cluster: its Kubernetes version, which known
// platform APIs it has, and its ingress and storage classes. Whatever
// cannot be read is left empty (the agent still connects).
func (s *Syncer) Capabilities(ctx context.Context) *fleetv1.Capabilities {
	out := &fleetv1.Capabilities{Apis: []string{CapabilityAI}, IngressClasses: []string{}, StorageClasses: []string{}}
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
			if s.AccessGate && slices.Contains(out.Apis, "traefik.io") {
				out.Apis = append(out.Apis, CapabilityAccess)
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
	out.ResourceKinds = resourceKinds(out)
	out.SecretStores = []string{}
	if slices.Contains(out.Apis, ExternalSecretsGroup) {
		out.SecretStores = s.secretStores(ctx, reader)
	}
	return out
}

// ExternalSecretsGroup is the API group of the External Secrets Operator.
const ExternalSecretsGroup = "external-secrets.io"

// secretStores names the ClusterSecretStores TI registered (sorted; empty
// when they cannot be read: the agent may lack the permission or the CRD).
func (s *Syncer) secretStores(ctx context.Context, reader client.Reader) []string {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: ExternalSecretsGroup, Version: "v1", Kind: "ClusterSecretStoreList"})
	out := []string{}
	if err := reader.List(ctx, list); err != nil {
		return out
	}
	for _, i := range list.Items {
		out = append(out, i.GetName())
	}
	slices.Sort(out)
	return out
}

// fenceAI rewrites the namespace's policies after an app that uses (or used)
// AI changed, so that odicea-ai opens (or closes) its gateway: the pods of an
// app with AI reach the gateway from their first start, whatever the cluster's
// egress mode. An error makes the caller retry the apply.
func (s *Syncer) fenceAI(ctx context.Context, namespace string, ai bool) error {
	if !ai || s.Namespaces == nil || s.Namespaces.Fence == nil {
		return nil
	}
	return s.Namespaces.Fence.Fence(ctx, namespace)
}
