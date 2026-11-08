package desired

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/controllers/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CNPGGroup is the API group of CloudNativePG (databases need it).
const CNPGGroup = "postgresql.cnpg.io"

// MaxResources bounds the resources one app uses.
const MaxResources = 10

// ValidResource checks a desired resource before the cluster is touched.
func ValidResource(r *fleetv1.Resource) error {
	switch {
	case r == nil:
		return errors.New("no resource")
	case !isUUID(r.GetEnvironmentId()) || !isUUID(r.GetOrganizationId()) || !isUUID(r.GetProjectId()):
		return errors.New("resource needs organization, project and environment ids")
	case r.GetGeneration() < 1:
		return errors.New("resource generation must be positive")
	}
	if err := resourcekind.CheckName(r.GetName()); err != nil {
		return err
	}
	if !resourcekind.Kind(r.GetKind()).Valid() {
		return fmt.Errorf("resource kind %q is not %s", r.GetKind(), resourcekind.KindsLabel())
	}
	if _, err := resourcekind.ParseStorage(r.GetStorage()); err != nil {
		return err
	}
	return nil
}

func validResourceRefs(refs []*fleetv1.ResourceRef) error {
	if len(refs) > MaxResources {
		return fmt.Errorf("app uses %d resources, at most %d", len(refs), MaxResources)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if err := resourcekind.CheckName(r.GetName()); err != nil {
			return err
		}
		if !resourcekind.Kind(r.GetKind()).Valid() {
			return fmt.Errorf("resource %q has kind %q, not %s", r.GetName(), r.GetKind(), resourcekind.KindsLabel())
		}
		if err := resourcekind.CheckPrefix(r.GetPrefix()); err != nil {
			return fmt.Errorf("resource %q: %w", r.GetName(), err)
		}
		if seen[r.GetName()] {
			return fmt.Errorf("resource %q is repeated", r.GetName())
		}
		seen[r.GetName()] = true
	}
	return nil
}

// resourcesOf maps the proto's resource references (sorted by name, so the
// same set always makes the same spec).
func resourcesOf(refs []*fleetv1.ResourceRef) []v1alpha1.ResourceRefSpec {
	if len(refs) == 0 {
		return nil
	}
	out := make([]v1alpha1.ResourceRefSpec, 0, len(refs))
	for _, r := range refs {
		out = append(out, v1alpha1.ResourceRefSpec{Name: r.GetName(), Kind: v1alpha1.ResourceKind(r.GetKind()), Prefix: r.GetPrefix(),
			Services: scopeOf(r.GetServices())})
	}
	slices.SortFunc(out, func(a, b v1alpha1.ResourceRefSpec) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// resourceKinds are what the cluster can provision: postgres needs the
// CloudNativePG API and a default storage class, redis and files a default
// storage class.
func resourceKinds(c *fleetv1.Capabilities) []string {
	out := []string{}
	if !slices.ContainsFunc(c.GetStorageClasses(), func(n string) bool { return strings.HasSuffix(n, " (default)") }) {
		return out
	}
	if slices.Contains(c.GetApis(), CNPGGroup) {
		out = append(out, string(resourcekind.Postgres))
	}
	return append(out, string(resourcekind.Redis), string(resourcekind.Files))
}

// ApplyResource writes the desired resource unless the cluster already has a
// newer generation, or the same one with the same spec. Errors are as
// Apply's: a plain one is retried, a *Rejected is final. A resource whose
// deletion the control plane started refuses to be made again until it is
// gone (the error is plain: the caller retries). One someone deleted in the
// cluster by hand keeps its data: a new spec for it is dropped (nil), and
// only deleted=true is taken, which lets the controller erase it.
func (s *Syncer) ApplyResource(ctx context.Context, r *fleetv1.Resource) error {
	if err := ValidResource(r); err != nil {
		return &Rejected{Message: err.Error()}
	}
	n := s.Namespaces
	if n == nil {
		n = &Namespaces{Client: s.Client}
	}
	if err := n.Ensure(ctx, r.GetOrganizationId(), r.GetProjectId(), r.GetEnvironmentId()); err != nil {
		return permanentResource(err)
	}
	labels := map[string]string{
		LabelOrganization: r.GetOrganizationId(), LabelProject: r.GetProjectId(), LabelEnvironment: r.GetEnvironmentId(),
	}
	spec := v1alpha1.OdysseyResourceSpec{
		Kind: v1alpha1.ResourceKind(r.GetKind()), Generation: r.GetGeneration(), Storage: r.GetStorage(), Deleted: r.GetDeleted(),
	}
	ns := Namespace(r.GetEnvironmentId())
	var cur v1alpha1.OdysseyResource
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: r.GetName()}, &cur)
	switch {
	case apierrors.IsNotFound(err):
		obj := &v1alpha1.OdysseyResource{ObjectMeta: metav1.ObjectMeta{Name: r.GetName(), Namespace: ns, Labels: labels}, Spec: spec}
		err = s.Client.Create(ctx, obj)
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("apply resource %s: created concurrently, retrying", r.GetName())
		}
	case err != nil:
		return permanentResource(fmt.Errorf("read resource %s: %w", r.GetName(), err))
	// These two come before any comparison of generations: the control plane
	// restarts them at 1 when a name is made again after the old resource was
	// forgotten, so a new spec can look older than the one being removed.
	case cur.Spec.Deleted && !spec.Deleted:
		// The control plane's deletion is in progress and its data is being
		// removed: the new one starts when that is done.
		return fmt.Errorf("resource %s is still being deleted, retrying", r.GetName())
	case !cur.DeletionTimestamp.IsZero() && !spec.Deleted:
		// Deleted in the cluster by hand: the agent keeps its data (the
		// controller reports it failed) until the control plane deletes it.
		// Nothing to apply, and retrying would never end.
		return nil
	case cur.Spec.Kind != spec.Kind:
		return &Rejected{Message: fmt.Sprintf("resource %s is a %s and cannot become a %s", r.GetName(), cur.Spec.Kind, spec.Kind)}
	case cur.Spec.Generation > spec.Generation:
		return nil // a newer generation already got here
	case cur.Spec.Generation == spec.Generation && equality.Semantic.DeepEqual(cur.Spec, spec):
		return nil
	default:
		// The size only grows; the rest the control plane decides. A deleted
		// flag is also set on an object that is terminating (updates of the
		// spec are allowed while the finalizer holds it): the teardown runs.
		old := cur.Spec.Storage
		cur.Spec = spec
		if o, err := resourcekind.ParseStorage(old); err == nil && o > resourcekind.MinStorageGi-1 {
			if n, _ := resourcekind.ParseStorage(spec.Storage); o > n {
				cur.Spec.Storage = old
			}
		}
		cur.Labels = mergeLabels(cur.Labels, labels)
		err = s.Client.Update(ctx, &cur)
	}
	return permanentResource(err)
}

func mergeLabels(cur, want map[string]string) map[string]string {
	if cur == nil {
		cur = map[string]string{}
	}
	for k, v := range want {
		cur[k] = v
	}
	return cur
}

func permanentResource(err error) error {
	if p := permanent(err); p != err {
		if r, ok := p.(*Rejected); ok {
			r.Message = strings.Replace(r.Message, "the cluster rejected the app", "the cluster rejected the resource", 1)
			r.Message = strings.Replace(r.Message, "the OdysseyApp resource is not", "the OdysseyResource resource is not", 1)
		}
		return p
	}
	return err
}

// RejectedResourceStatus is how a rejected resource is reported.
func RejectedResourceStatus(r *fleetv1.Resource, err error) *fleetv1.ResourceStatus {
	return &fleetv1.ResourceStatus{
		EnvironmentId: r.GetEnvironmentId(), Name: r.GetName(), ObservedGeneration: r.GetGeneration(),
		Phase: string(v1alpha1.ResourceFailed), Message: clip(err.Error(), maxMessage),
	}
}

// ResourceStatuses lists what the resource controller observed of every
// resource the agent applied (the ones it has not acted on since their last
// change are left out).
func (s *Syncer) ResourceStatuses(ctx context.Context) ([]*fleetv1.ResourceStatus, error) {
	var list v1alpha1.OdysseyResourceList
	if err := s.Client.List(ctx, &list, client.HasLabels{LabelEnvironment}); err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	out := make([]*fleetv1.ResourceStatus, 0, len(list.Items))
	for i := range list.Items {
		if st := resourceStatusOf(&list.Items[i]); st != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

// ResourceStatus is one resource's (nil when it does not exist or was not
// acted on yet).
func (s *Syncer) ResourceStatus(ctx context.Context, environment, name string) (*fleetv1.ResourceStatus, error) {
	var o v1alpha1.OdysseyResource
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: Namespace(environment), Name: name}, &o); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("status of resource %s: %w", name, err)
	}
	return resourceStatusOf(&o), nil
}

// ResourceReported records that the control plane was told st, a deleted
// resource: only then does the controller remove the resource's object.
// Anything else, or a resource that is already gone, is a no-op.
func (s *Syncer) ResourceReported(ctx context.Context, st *fleetv1.ResourceStatus) error {
	if st.GetPhase() != string(v1alpha1.ResourceDeleted) {
		return nil
	}
	var o v1alpha1.OdysseyResource
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: Namespace(st.GetEnvironmentId()), Name: st.GetName()}, &o)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	mark := fmt.Sprint(st.GetObservedGeneration())
	if o.Annotations[resource.AnnotationReported] == mark {
		return nil
	}
	if o.Annotations == nil {
		o.Annotations = map[string]string{}
	}
	o.Annotations[resource.AnnotationReported] = mark
	return client.IgnoreNotFound(s.Client.Update(ctx, &o))
}

func resourceStatusOf(o *v1alpha1.OdysseyResource) *fleetv1.ResourceStatus {
	env := o.Labels[LabelEnvironment]
	st := o.Status
	if !isUUID(env) || o.Namespace != Namespace(env) || st.ObservedGeneration < 1 || st.ObservedGeneration != o.Spec.Generation {
		return nil // not ours, or the spec changed after this status: not fresh yet
	}
	switch st.Phase {
	case v1alpha1.ResourceProvisioning, v1alpha1.ResourceReady, v1alpha1.ResourceFailed, v1alpha1.ResourceDeleted:
	default:
		return nil
	}
	return &fleetv1.ResourceStatus{
		EnvironmentId: env, Name: o.Name, ObservedGeneration: st.ObservedGeneration,
		Phase: string(st.Phase), Message: clip(st.Message, maxMessage),
	}
}

// aiOf maps the proto's AI to the CRD's (nil: the app has none).
func aiOf(a *fleetv1.AI) *v1alpha1.AISpec {
	if a == nil || a.GetBaseUrl() == "" {
		return nil
	}
	return &v1alpha1.AISpec{BaseURL: a.GetBaseUrl(), KeySecret: a.GetKeySecret(), Services: scopeOf(a.GetServices())}
}
