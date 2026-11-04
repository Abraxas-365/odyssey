package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/secretstore"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Secret variables (phase 2a). A managed secret's value lives in the Secret
// <app>-secrets (agent/secretstore writes it when the console delivers it); an
// external one is read by the External Secrets Operator into the Secret
// <app>-external-<store> (one ExternalSecret per store, because an
// ExternalSecret has exactly one). Pods read both as environment variables.
// The agent manages ExternalSecrets as unstructured objects (the ESO Go
// module is not a dependency) and reads Secrets from the API, never a cache.
// Secrets: get (gate, prune, adopt), update (prune, owner reference) and
// delete (a Secret pruned empty); secretstore adds create. No list or watch,
// and only in the environments' namespaces (desired.SecretsRole).
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;create;update;delete

const (
	// AnnotationSecrets is on the pod template: a hash of the app's secret
	// references, so a new version of a secret rolls the pods.
	AnnotationSecrets = "odicea.dev/secrets"
	// annotationFingerprint is on an ExternalSecret: a hash of the spec the
	// agent wrote (ESO fills in defaults, so the live spec is not compared).
	annotationFingerprint = "odicea.dev/fingerprint"
	// annotationSecretsWaiting is on the OdysseyApp: "<revision>/<generation>/<RFC 3339>",
	// when that revision first waited for a secret (it survives restarts).
	annotationSecretsWaiting = "odicea.dev/secrets-waiting"

	// secretWait is how long a revision waits for its secrets before it fails.
	secretWait = 2 * time.Minute

	// secretGrace is how long a delivered value stays in <app>-secrets even
	// though the Ready revision does not list it. The control plane delivers a
	// value as soon as a person loads it, but the revision that lists it can
	// arrive much later (deployments queue). Meanwhile any reconcile of the
	// old Ready revision prunes: without the grace it would delete the value
	// just delivered, the new revision would wait and fail "Falta el
	// secreto", and nothing would send the value again (it was acknowledged).
	// An hour is far longer than a queued deployment waits; a key with no
	// recorded delivery time (written before this existed) counts as old.
	secretGrace = time.Hour
	// NoExternalSecretsMessage is what an app with an external secret reports
	// in a cluster that has no External Secrets Operator (reason config).
	NoExternalSecretsMessage = "Este clúster no tiene un gestor de secretos externos (External Secrets Operator). Pide a TI que lo instale."
)

var externalSecretGVK = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ExternalSecret"}

// NewExternalSecret is an empty ExternalSecret (to watch or read).
func NewExternalSecret() *unstructured.Unstructured {
	e := &unstructured.Unstructured{}
	e.SetGroupVersionKind(externalSecretGVK)
	return e
}

func newExternalSecretList() *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(schema.GroupVersionKind{Group: externalSecretGVK.Group, Version: externalSecretGVK.Version, Kind: "ExternalSecretList"})
	return l
}

// externalName is the ExternalSecret of app a for store, and the Secret it
// makes.
func externalName(a *v1alpha1.OdysseyApp, store string) string {
	return a.Name + "-external-" + store
}

// sortedSecrets are a's secret references by name.
func sortedSecrets(a *v1alpha1.OdysseyApp) []v1alpha1.SecretRefSpec {
	out := slices.Clone(a.Spec.Secrets)
	slices.SortFunc(out, func(x, y v1alpha1.SecretRefSpec) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// externalStores groups a's external references by store.
func externalStores(a *v1alpha1.OdysseyApp) map[string][]v1alpha1.SecretRefSpec {
	out := map[string][]v1alpha1.SecretRefSpec{}
	for _, s := range sortedSecrets(a) {
		if s.Source == v1alpha1.SecretExternal {
			out[s.Store] = append(out[s.Store], s)
		}
	}
	return out
}

// secretEnv are the variables the pod reads from Secrets, by name.
func secretEnv(a *v1alpha1.OdysseyApp) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, s := range sortedSecrets(a) {
		secret := secretstore.Name(a.Name)
		if s.Source == v1alpha1.SecretExternal {
			secret = externalName(a, s.Store)
		}
		out = append(out, corev1.EnvVar{Name: s.Name, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: s.Name},
		}})
	}
	return out
}

// secretsHash identifies a's secret references (empty when there are none, so
// apps without secrets keep their pod template).
func secretsHash(a *v1alpha1.OdysseyApp) string {
	refs := sortedSecrets(a)
	if len(refs) == 0 {
		return ""
	}
	h := sha256.New()
	for _, s := range refs {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\x00%s\x00%s\n", s.Name, s.Source, s.Version, s.Store, s.Key, s.Property)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// externalSecretsAvailable says whether the cluster serves the ExternalSecret
// kind (the External Secrets Operator is installed).
func (r *Reconciler) externalSecretsAvailable() bool {
	m := r.ExternalSecrets
	if m == nil {
		m = r.Client.RESTMapper()
	}
	_, err := m.RESTMapping(externalSecretGVK.GroupKind(), externalSecretGVK.Version)
	return err == nil
}

// externalSecret is the ExternalSecret that reads refs (all of one store).
func externalSecret(a *v1alpha1.OdysseyApp, store string, refs []v1alpha1.SecretRefSpec) *unstructured.Unstructured {
	data := make([]any, 0, len(refs))
	for _, s := range refs {
		remote := map[string]any{"key": s.Key}
		if s.Property != "" {
			remote["property"] = s.Property
		}
		data = append(data, map[string]any{"secretKey": s.Name, "remoteRef": remote})
	}
	e := NewExternalSecret()
	e.SetName(externalName(a, store))
	e.SetNamespace(a.Namespace)
	e.Object["spec"] = map[string]any{
		"refreshInterval": "1h",
		"secretStoreRef":  map[string]any{"kind": "ClusterSecretStore", "name": store},
		"target":          map[string]any{"name": externalName(a, store), "creationPolicy": "Owner"},
		"data":            data,
	}
	return e
}

func fingerprint(e *unstructured.Unstructured) string {
	raw, _ := json.Marshal(e.Object["spec"]) // maps and strings always marshal
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:32]
}

// notOurs is an object in the way: it has the name of one of a's but the
// agent does not manage it.
type notOurs struct{ kind, name, namespace string }

func (n *notOurs) Error() string {
	return fmt.Sprintf("a %s named %s already exists in %s and is not managed by Odyssey", n.kind, n.name, n.namespace)
}

// putExternal creates or updates a's ExternalSecret for store.
func (r *Reconciler) putExternal(ctx context.Context, a *v1alpha1.OdysseyApp, store string, refs []v1alpha1.SecretRefSpec) error {
	want := externalSecret(a, store, refs)
	want.SetLabels(labels(a))
	want.SetAnnotations(map[string]string{annotationFingerprint: fingerprint(want)})
	if err := controllerutil.SetControllerReference(a, want, r.Client.Scheme()); err != nil {
		return err
	}
	cur := NewExternalSecret()
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(want), cur)
	switch {
	case apierrors.IsNotFound(err):
		return r.Client.Create(ctx, want)
	case err != nil:
		return err
	case !metav1.IsControlledBy(cur, a):
		return &notOurs{"ExternalSecret", cur.GetName(), cur.GetNamespace()}
	case cur.GetAnnotations()[annotationFingerprint] == want.GetAnnotations()[annotationFingerprint]:
		return nil
	}
	cur.Object["spec"] = want.Object["spec"]
	cur.SetLabels(want.GetLabels())
	ann := cur.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[annotationFingerprint] = want.GetAnnotations()[annotationFingerprint]
	cur.SetAnnotations(ann)
	return r.Client.Update(ctx, cur)
}

// dropExternals deletes the ExternalSecrets of a that are no longer wanted
// (the Secrets they made go with them: ESO owns those).
func (r *Reconciler) dropExternals(ctx context.Context, a *v1alpha1.OdysseyApp) error {
	if !r.externalSecretsAvailable() {
		return nil
	}
	list := newExternalSecretList()
	if err := r.Client.List(ctx, list, client.InNamespace(a.Namespace), client.MatchingLabels{LabelManagedBy: ManagedBy, LabelApp: a.Name}); err != nil {
		return err
	}
	keep := map[string]bool{}
	for store := range externalStores(a) {
		keep[externalName(a, store)] = true
	}
	for i := range list.Items {
		e := &list.Items[i]
		if keep[e.GetName()] || !metav1.IsControlledBy(e, a) {
			continue
		}
		if err := r.Client.Delete(ctx, e); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// hasManaged says a's spec lists a managed secret.
func hasManaged(a *v1alpha1.OdysseyApp) bool {
	for _, s := range a.Spec.Secrets {
		if s.Source == v1alpha1.SecretManaged {
			return true
		}
	}
	return false
}

// adoptManaged makes a the owner of <app>-secrets, so deleting the app deletes
// its values. The store writes the Secret when a value arrives, which can be
// before the OdysseyApp exists, so it is adopted here. A plain owner
// reference (not a controller one, which would block the owner's deletion
// and need the finalizers permission) is enough for garbage collection.
// A Secret of that name the agent does not manage is never touched: when the
// spec needs managed secrets it is an error (*notOurs), otherwise it is none
// of the app's business.
func (r *Reconciler) adoptManaged(ctx context.Context, a *v1alpha1.OdysseyApp) error {
	var sec corev1.Secret
	if err := r.API.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: secretstore.Name(a.Name)}, &sec); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !secretstore.Ours(&sec) {
		if hasManaged(a) {
			return &notOurs{"Secret", sec.Name, sec.Namespace}
		}
		return nil
	}
	for _, o := range sec.OwnerReferences {
		if o.UID == a.UID {
			return nil
		}
	}
	if err := controllerutil.SetOwnerReference(a, &sec, r.Client.Scheme()); err != nil {
		return err
	}
	return r.Client.Update(ctx, &sec)
}

// pruneManaged removes from <app>-secrets what the spec no longer lists (and
// the versions and delivery times recorded for it), and deletes the Secret
// when no key is left. A value delivered less than secretGrace ago stays even
// if the spec does not list it; when one does, the result is the time to
// look again (zero: nothing waits). A Secret the agent does not manage is
// left alone.
func (r *Reconciler) pruneManaged(ctx context.Context, a *v1alpha1.OdysseyApp) (time.Duration, error) {
	keep := map[string]bool{}
	for _, s := range a.Spec.Secrets {
		if s.Source == v1alpha1.SecretManaged {
			keep[s.Name] = true
		}
	}
	var sec corev1.Secret
	if err := r.API.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: secretstore.Name(a.Name)}, &sec); err != nil {
		return 0, client.IgnoreNotFound(err)
	}
	if !secretstore.Ours(&sec) {
		return 0, nil
	}
	now := r.Now()
	versions, delivered := secretstore.Versions(&sec), secretstore.Delivered(&sec)
	var wait time.Duration
	changed := false
	graced := map[string]bool{}
	for k := range sec.Data {
		if keep[k] {
			continue
		}
		// No recorded time: written before the grace existed, so old. A
		// time in the future (a skewed clock) counts as old as well: it must
		// not keep a value forever.
		if at, ok := delivered[k]; ok {
			if age := now.Sub(at); age >= 0 && age < secretGrace {
				graced[k] = true
				if left := secretGrace - age; wait == 0 || left < wait {
					wait = left
				}
				continue
			}
		}
		delete(sec.Data, k)
		changed = true
	}
	for k := range versions {
		if !keep[k] && !graced[k] {
			delete(versions, k)
			changed = true
		}
	}
	for k := range delivered {
		if !keep[k] && !graced[k] {
			delete(delivered, k)
			changed = true
		}
	}
	if len(sec.Data) == 0 {
		// Nothing left to serve: no empty Secret. The preconditions keep a
		// value delivered since we read it from going with it.
		err := r.Client.Delete(ctx, &sec, client.Preconditions{UID: &sec.UID, ResourceVersion: &sec.ResourceVersion})
		if apierrors.IsNotFound(err) {
			return wait, nil
		}
		return wait, err
	}
	if !changed {
		return wait, nil
	}
	secretstore.SetVersions(&sec, versions)
	secretstore.SetDelivered(&sec, delivered)
	return wait, r.Client.Update(ctx, &sec)
}

// missingSecret is the first of a's secrets (by name) the cluster cannot
// serve yet: a managed one the store does not hold at the wanted version, or
// an external one whose Secret is not there or lacks the key.
func (r *Reconciler) missingSecret(ctx context.Context, a *v1alpha1.OdysseyApp) (*v1alpha1.SecretRefSpec, error) {
	have := map[string]*corev1.Secret{} // by Secret name; nil: absent
	read := func(name string) (*corev1.Secret, error) {
		if s, ok := have[name]; ok {
			return s, nil
		}
		var s corev1.Secret
		err := r.API.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, &s)
		switch {
		case apierrors.IsNotFound(err):
			have[name] = nil
			return nil, nil
		case err != nil:
			return nil, err
		}
		have[name] = &s
		return &s, nil
	}
	for _, ref := range sortedSecrets(a) {
		name, version := secretstore.Name(a.Name), ref.Version
		if ref.Source == v1alpha1.SecretExternal {
			name, version = externalName(a, ref.Store), 0
		}
		s, err := read(name)
		if err != nil {
			return nil, err
		}
		if s == nil {
			return &ref, nil
		}
		if _, ok := s.Data[ref.Name]; !ok {
			return &ref, nil
		}
		if ref.Source == v1alpha1.SecretManaged && secretstore.Versions(s)[ref.Name] < version {
			return &ref, nil
		}
	}
	return nil, nil
}

// waited says a's current revision and spec waited for a secret.
func (r *Reconciler) waited(a *v1alpha1.OdysseyApp) bool {
	return strings.HasPrefix(a.Annotations[annotationSecretsWaiting], fmt.Sprintf("%d/%d/", a.Spec.Revision, a.Generation))
}

// waitingSince is when a's revision first waited for its secrets, recording
// now on the OdysseyApp the first time (so the window survives restarts).
func (r *Reconciler) waitingSince(ctx context.Context, a *v1alpha1.OdysseyApp) (time.Time, error) {
	prefix := fmt.Sprintf("%d/%d/", a.Spec.Revision, a.Generation)
	if v, ok := strings.CutPrefix(a.Annotations[annotationSecretsWaiting], prefix); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
	}
	now := r.Now()
	if a.Annotations == nil {
		a.Annotations = map[string]string{}
	}
	a.Annotations[annotationSecretsWaiting] = prefix + now.UTC().Format(time.RFC3339)
	return now, r.Client.Update(ctx, a)
}

// settled reports that a's status already is the final verdict of its
// current revision and spec (see report).
func settled(a *v1alpha1.OdysseyApp) bool {
	s := a.Status
	return s.ObservedRevision == a.Spec.Revision && s.ObservedGeneration == a.Generation && (s.Phase == v1alpha1.PhaseFailed || s.Phase == v1alpha1.PhaseReady)
}

// secretsStep readies a's secrets before its Deployment is applied: it makes
// the ExternalSecrets and holds the rollout until every secret can be read
// (the running version keeps serving: the Deployment is not touched). done
// says the pass must stop there (with res, err).
func (r *Reconciler) secretsStep(ctx context.Context, a *v1alpha1.OdysseyApp) (done bool, res ctrl.Result, err error) {
	fail := func(msg string) (bool, ctrl.Result, error) {
		return true, ctrl.Result{}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseFailed, Reason: v1alpha1.ReasonConfig, Message: msg})
	}
	stores := externalStores(a)
	if err := r.adoptManaged(ctx, a); err != nil {
		var n *notOurs
		if errors.As(err, &n) {
			return fail(n.Error())
		}
		return true, ctrl.Result{}, err
	}
	if len(stores) > 0 {
		if !r.externalSecretsAvailable() {
			return fail(NoExternalSecretsMessage)
		}
		names := make([]string, 0, len(stores))
		for s := range stores {
			names = append(names, s)
		}
		slices.Sort(names)
		for _, store := range names {
			if err := r.putExternal(ctx, a, store, stores[store]); err != nil {
				var n *notOurs
				switch {
				case errors.As(err, &n):
					return fail(n.Error())
				case apierrors.IsInvalid(err):
					return fail(firstLine(err.Error(), ""))
				}
				return true, ctrl.Result{}, err
			}
		}
	}

	// A revision that is settled was judged when it was applied: a secret
	// that goes away later is the app's trouble, and holding nothing keeps
	// it serving. One that failed waiting for a secret stays held:
	// its Deployment is never applied late, behind the verdict the control
	// plane already has.
	if settled(a) {
		return a.Status.Phase == v1alpha1.PhaseFailed && r.waited(a), ctrl.Result{}, nil
	}
	missing, err := r.missingSecret(ctx, a)
	if err != nil {
		return true, ctrl.Result{}, err
	}
	if missing != nil {
		since, err := r.waitingSince(ctx, a)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if r.Now().Sub(since) >= secretWait {
			if missing.Source == v1alpha1.SecretExternal {
				return fail(fmt.Sprintf("No se pudo leer el secreto externo %s (revisa la referencia en el almacén %s)", missing.Name, missing.Store))
			}
			return fail(fmt.Sprintf("Falta el secreto %s: cárgalo en la consola", missing.Name))
		}
		return true, ctrl.Result{RequeueAfter: progressEvery}, r.report(ctx, a, Observation{Phase: v1alpha1.PhaseProgressing, Message: "Esperando el secreto " + missing.Name})
	}
	if _, ok := a.Annotations[annotationSecretsWaiting]; ok {
		delete(a.Annotations, annotationSecretsWaiting)
		if err := r.Client.Update(ctx, a); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	return false, ctrl.Result{}, nil
}

// pruneSecrets removes what a's spec no longer lists once its rollout is
// ready (the previous version's pods, which may still restart on the old
// references, are gone by then). The result is when to look again because a
// recently delivered value was kept (zero: never).
func (r *Reconciler) pruneSecrets(ctx context.Context, a *v1alpha1.OdysseyApp) (time.Duration, error) {
	wait, err := r.pruneManaged(ctx, a)
	if err != nil {
		return 0, err
	}
	return wait, r.dropExternals(ctx, a)
}
