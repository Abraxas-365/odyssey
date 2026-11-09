package resource

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/externalsecret"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// The existing engine: a resource the company already has (spec.existing).
// It never makes, changes or deletes the resource itself; it writes only
// the kind's connection Secret, the one the built-in engine would (kind.secret
// and kind.keys), so the app controller wires both alike.
//
//   - Values that are all written in the recipe (no reference): the agent
//     writes the Secret itself.
//   - Any reference to a secret store: an ExternalSecret of the same name
//     writes it (creationPolicy Owner: deleting the ExternalSecret deletes the
//     Secret). Each reference is one data entry v<i> (i: the variable's
//     place in the contract, never a name from the recipe), read from its
//     own ClusterSecretStore; the template puts every key together. A
//     written value goes into the template as a quoted Go string, so it is
//     text and never runs. A derived variable (DATABASE_URL) is built from
//     the others with its user, password and database percent-encoded.
//
// ESO refreshes the Secret every hour; pods read it when they start, so a
// value rotated in the store reaches the app at its next deployment.
//
// It reads ExternalSecrets from the API (never cached; the app controller
// already holds their RBAC) and needs no watch: a resource that is not
// ready is looked at again every few seconds.

const (
	// NoExternalSecretsMessage: a resource reads from a secret store where
	// the External Secrets Operator is not installed.
	NoExternalSecretsMessage = "this cluster has no External Secrets Operator to read the connection from a secret store"
	// SyncFailedMessage is every failure ESO reports, in one fixed phrase
	// (its own message may name the store's internals).
	SyncFailedMessage = "the secret store did not give every value of the connection: check the recipe's store, keys and properties"
	syncingMessage    = "reading the connection from the secret store"
	switchingMessage  = "replacing the previous connection"
	refreshInterval   = "1h"
)

// part is one piece of a derived variable: text, or the value of another
// variable (escaped for a URL, or as a URL's host). when names a variable
// the part needs (an optional one the resource may not give).
type part struct {
	text     string
	variable string
	escape   bool
	host     bool
	when     string
}

// piece is a rendered part: written text, or data entry ref (-1: none).
type piece struct {
	text   string
	ref    int
	escape bool
	host   bool
}

// storeRef is one reference of the connection: data entry v<index>.
type storeRef struct {
	index int
	ref   resourcespec.Ref
}

// connection is what the Secret holds: each key's pieces (a key whose
// optional variable the resource does not give is absent) and the store
// references they read.
type connection struct {
	keys map[string][]piece
	refs []storeRef
}

// existingOf is o's spec.existing as both planes describe it.
func existingOf(o *v1alpha1.OdysseyResource) resourcespec.Existing {
	out := resourcespec.Existing{Values: make([]resourcespec.Value, 0, len(o.Spec.Existing.Values))}
	for _, v := range o.Spec.Existing.Values {
		x := resourcespec.Value{Variable: v.Variable, Value: v.Value}
		if v.Ref != nil {
			x.Ref = &resourcespec.Ref{Store: v.Ref.Store, Key: v.Ref.Key, Property: v.Ref.Property}
		}
		out.Values = append(out.Values, x)
	}
	return out
}

// connectionOf renders e (valid for kind k) into the keys of k's Secret.
func connectionOf(k kind, kindName resourcekind.Kind, e resourcespec.Existing) (connection, error) {
	c := resourcekind.Of(kindName)
	if len(c.Variables) != len(k.keys) {
		return connection{}, fmt.Errorf("this agent cannot write a %s connection: update the agent", kindName)
	}
	byName := map[string]resourcespec.Value{}
	for _, v := range e.Values {
		byName[v.Variable] = v
	}
	out := connection{keys: map[string][]piece{}}
	index := map[string]int{}
	for i, v := range c.Variables {
		index[v.Name] = i
		if x, ok := byName[v.Name]; ok && x.Ref != nil {
			out.refs = append(out.refs, storeRef{index: i, ref: *x.Ref})
		}
	}
	value := func(name string, escape, host bool) (piece, bool) {
		x, ok := byName[name]
		switch {
		case !ok:
			return piece{}, false
		case x.Ref != nil:
			return piece{ref: index[name], escape: escape, host: host}, true
		case escape:
			return piece{text: urlEscape(x.Value), ref: -1}, true
		case host && strings.Contains(x.Value, ":"):
			return piece{text: "[" + x.Value + "]", ref: -1}, true
		}
		return piece{text: x.Value, ref: -1}, true
	}
	for i, v := range c.Variables {
		key := k.keys[i]
		if !v.Derived {
			if p, ok := value(v.Name, false, false); ok {
				out.keys[key] = []piece{p}
			}
			continue
		}
		parts, ok := k.derived[v.Name]
		if !ok {
			return connection{}, fmt.Errorf("this agent cannot build %s: update the agent", v.Name)
		}
		var ps []piece
		for _, p := range parts {
			if _, given := byName[p.when]; p.when != "" && !given {
				continue
			}
			if p.variable == "" {
				ps = append(ps, piece{text: p.text, ref: -1})
				continue
			}
			x, ok := value(p.variable, p.escape, p.host)
			if !ok {
				return connection{}, fmt.Errorf("%s needs %s", v.Name, p.variable)
			}
			ps = append(ps, x)
		}
		out.keys[key] = ps
	}
	return out, nil
}

// urlEscape encodes s for a URL's user, password or path the way the
// template does (externalsecret.Escaped): every reserved character, a space
// as %20.
func urlEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// written is the value of a connection without references.
func (c connection) written() map[string][]byte {
	out := make(map[string][]byte, len(c.keys))
	for key, ps := range c.keys {
		var b strings.Builder
		for _, p := range ps {
			b.WriteString(p.text)
		}
		out[key] = []byte(b.String())
	}
	return out
}

// template is each key as an ESO template (engineVersion v2).
func (c connection) template() map[string]any {
	out := make(map[string]any, len(c.keys))
	for key, ps := range c.keys {
		var b strings.Builder
		for _, p := range ps {
			if p.ref < 0 {
				b.WriteString(externalsecret.Action(externalsecret.Literal(p.text)))
				continue
			}
			d := ".v" + strconv.Itoa(p.ref)
			switch {
			case p.escape:
				b.WriteString(externalsecret.Action(externalsecret.Escaped(d)))
			case p.host:
				// An IPv6 address goes in brackets.
				b.WriteString(`{{ if contains ":" ` + d + ` }}[{{ ` + d + ` }}]{{ else }}{{ ` + d + ` }}{{ end }}`)
			default:
				b.WriteString(externalsecret.Action(d))
			}
		}
		out[key] = b.String()
	}
	return out
}

// externalSecret is the ExternalSecret that writes c into Secret name.
func (c connection) externalSecret(o *v1alpha1.OdysseyResource, name string) *unstructured.Unstructured {
	data := make([]any, 0, len(c.refs))
	for _, r := range c.refs {
		remote := map[string]any{"key": r.ref.Key}
		if r.ref.Property != "" {
			remote["property"] = r.ref.Property
		}
		data = append(data, map[string]any{
			"secretKey": "v" + strconv.Itoa(r.index), "remoteRef": remote,
			"sourceRef": map[string]any{"storeRef": externalsecret.StoreRef(r.ref.Store)},
		})
	}
	lbl := map[string]any{}
	for k, v := range labels(o) {
		lbl[k] = v
	}
	e := externalsecret.New()
	e.SetName(name)
	e.SetNamespace(o.Namespace)
	e.SetLabels(labels(o))
	e.Object["spec"] = map[string]any{
		"refreshInterval": refreshInterval,
		"target": map[string]any{
			"name": name, "creationPolicy": "Owner",
			"template": map[string]any{"engineVersion": "v2", "metadata": map[string]any{"labels": lbl}, "data": c.template()},
		},
		"data": data,
	}
	externalsecret.Stamp(e)
	return e
}

func (r *Reconciler) externalSecretsServed() bool {
	m := r.Mapper
	if m == nil {
		m = r.Client.RESTMapper()
	}
	return externalsecret.Served(m)
}

// existing writes o's connection and says whether the app can read it.
func (r *Reconciler) existing(ctx context.Context, o *v1alpha1.OdysseyResource) (observation, error) {
	kindName := resourcekind.Kind(o.Spec.Kind)
	k, ok := kinds[o.Spec.Kind]
	if !ok || k.secret == nil {
		return failed(fmt.Sprintf("a %s resource cannot be one the company already has", o.Spec.Kind)), nil
	}
	e := existingOf(o)
	if err := (resourcespec.Spec{Existing: &e}).Validate(kindName); err != nil {
		return failed(err.Error()), nil
	}
	c, err := connectionOf(k, kindName, e)
	if err != nil {
		return failed(err.Error()), nil
	}
	name := k.secret(o.Name)
	if len(c.refs) == 0 {
		return r.writtenConnection(ctx, o, name, c)
	}
	if !r.externalSecretsServed() {
		return failed(NoExternalSecretsMessage), nil
	}
	// A Secret this engine wrote itself (a version of the recipe without
	// references) makes way for the one ESO writes.
	var sec corev1.Secret
	switch err := r.API.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: name}, &sec); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return observation{}, err
	case metav1.IsControlledBy(&sec, o):
		if sec.DeletionTimestamp == nil {
			if err := r.Client.Delete(ctx, &sec); client.IgnoreNotFound(err) != nil {
				return observation{}, err
			}
		}
		return provisioning(switchingMessage), nil
	case !controlledByExternalSecret(&sec, name):
		return observation{}, &notOurs{"Secret", name}
	}
	want := c.externalSecret(o, name)
	if err := controllerutil.SetControllerReference(o, want, r.Client.Scheme()); err != nil {
		return observation{}, err
	}
	live := externalsecret.New()
	switch err := r.API.Get(ctx, client.ObjectKeyFromObject(want), live); {
	case apierrors.IsNotFound(err):
		externalsecret.Written(want, r.now())
		if err := r.Client.Create(ctx, want); err != nil {
			return observation{}, err
		}
		return provisioning(syncingMessage), nil
	case err != nil:
		return observation{}, err
	case !metav1.IsControlledBy(live, o):
		return observation{}, &notOurs{externalsecret.GVK.Kind, name}
	case !externalsecret.Same(live, want):
		live.Object["spec"] = want.Object["spec"]
		live.SetLabels(want.GetLabels())
		ann := live.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[externalsecret.AnnotationFingerprint] = want.GetAnnotations()[externalsecret.AnnotationFingerprint]
		live.SetAnnotations(ann)
		externalsecret.Written(live, r.now())
		if err := r.Client.Update(ctx, live); err != nil {
			return observation{}, err
		}
		// ESO judges the new spec a moment later.
		return provisioning(syncingMessage), nil
	}
	switch externalsecret.StateOf(live, r.now()) {
	case externalsecret.Synced, externalsecret.Stale:
		// Stale: a refresh failed after the current spec synced (a store
		// briefly unreachable); the Secret keeps what it read.
		return ready(), nil
	case externalsecret.Failed:
		return failed(SyncFailedMessage), nil
	}
	return provisioning(syncingMessage), nil
}

// controlledByExternalSecret says ESO wrote s for the ExternalSecret name.
func controlledByExternalSecret(s *corev1.Secret, name string) bool {
	c := metav1.GetControllerOf(s)
	return c != nil && c.Kind == externalsecret.GVK.Kind && c.Name == name && strings.HasPrefix(c.APIVersion, externalsecret.Group+"/")
}

// writtenConnection writes a connection without references as the Secret
// name (no store, no ESO).
func (r *Reconciler) writtenConnection(ctx context.Context, o *v1alpha1.OdysseyResource, name string, c connection) (observation, error) {
	// The ExternalSecret of a version that had references goes first (the
	// Secret goes with it).
	if r.externalSecretsServed() {
		es := externalsecret.New()
		switch err := r.API.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: name}, es); {
		case apierrors.IsNotFound(err):
		case err != nil:
			return observation{}, err
		case metav1.IsControlledBy(es, o):
			if es.GetDeletionTimestamp() == nil {
				if err := r.Client.Delete(ctx, es, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
					return observation{}, err
				}
			}
			return provisioning(switchingMessage), nil
		default:
			// someone else's: never taken over, and its Secret never waited for
			return observation{}, &notOurs{externalsecret.GVK.Kind, name}
		}
	}
	data := c.written()
	var cur corev1.Secret
	switch err := r.API.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: name}, &cur); {
	case apierrors.IsNotFound(err):
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: o.Namespace, Labels: labels(o)}, Type: corev1.SecretTypeOpaque, Data: data}
		if err := controllerutil.SetControllerReference(o, s, r.Client.Scheme()); err != nil {
			return observation{}, err
		}
		if err := r.Client.Create(ctx, s); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return observation{}, errCacheBehind
			}
			return observation{}, err
		}
		return ready(), nil
	case err != nil:
		return observation{}, err
	case controlledByExternalSecret(&cur, name):
		return provisioning(switchingMessage), nil // ESO's, going with its ExternalSecret
	case !metav1.IsControlledBy(&cur, o):
		return observation{}, &notOurs{"Secret", name}
	case sameData(cur.Data, data):
		return ready(), nil
	}
	cur.Data = data
	cur.Labels = labels(o)
	if err := r.Client.Update(ctx, &cur); err != nil {
		return observation{}, err
	}
	return ready(), nil
}

func sameData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || string(w) != string(v) {
			return false
		}
	}
	return true
}

// removeExisting deletes the connection: the ExternalSecret (ESO's Secret
// goes with it) or the Secret the engine wrote itself. The company's
// resource is never touched.
func (r *Reconciler) removeExisting(_ context.Context, o *v1alpha1.OdysseyResource, del func(client.Object) error) (bool, error) {
	k, ok := kinds[o.Spec.Kind]
	if !ok || k.secret == nil {
		return false, nil
	}
	name := k.secret(o.Name)
	if r.externalSecretsServed() {
		es := externalsecret.New()
		es.SetName(name)
		es.SetNamespace(o.Namespace)
		if err := del(es); err != nil {
			return false, err
		}
	}
	return false, del(&corev1.Secret{ObjectMeta: objectKey(o, name)})
}
