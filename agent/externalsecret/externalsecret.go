// Package externalsecret is what the agent knows of the External Secrets
// Operator (ESO): the ExternalSecret kind it writes, whether the cluster
// serves it, how to tell that one was written as wanted and has synced, and
// how a value is put in its template safely. The app controller (an app's
// external secrets) and the resource controller (the connection of a
// resource the company already has) both use it; no other package of the
// agent names ESO's API (an architecture test holds it to that).
//
// ESO is driven as unstructured objects: its Go module is not a dependency.
package externalsecret

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group is ESO's API group.
const Group = "external-secrets.io"

// GVK is the ExternalSecret kind (external-secrets.io/v1: ESO 0.17 and later).
var GVK = schema.GroupVersionKind{Group: Group, Version: "v1", Kind: "ExternalSecret"}

// StoreListGVK lists ClusterSecretStores (the stores IT registered).
var StoreListGVK = schema.GroupVersionKind{Group: Group, Version: "v1", Kind: "ClusterSecretStoreList"}

// AnnotationFingerprint is on an ExternalSecret: a hash of the spec the
// agent wrote (ESO fills in defaults, so the live spec is not compared).
const AnnotationFingerprint = "odicea.dev/fingerprint"

// New is an empty ExternalSecret (to write, watch or read).
func New() *unstructured.Unstructured {
	e := &unstructured.Unstructured{}
	e.SetGroupVersionKind(GVK)
	return e
}

// NewList is an empty list of ExternalSecrets.
func NewList() *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(schema.GroupVersionKind{Group: GVK.Group, Version: GVK.Version, Kind: GVK.Kind + "List"})
	return l
}

// Served says whether the cluster serves the ExternalSecret kind (ESO is
// installed). The mapper discovers lazily, so an ESO installed after the
// agent started is found.
func Served(m meta.RESTMapper) bool {
	_, err := m.RESTMapping(GVK.GroupKind(), GVK.Version)
	return err == nil
}

// Fingerprint is a short hash of e's spec.
func Fingerprint(e *unstructured.Unstructured) string {
	raw, _ := json.Marshal(e.Object["spec"]) // maps and strings always marshal
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:32]
}

// Stamp records e's fingerprint on it (call once its spec is final).
func Stamp(e *unstructured.Unstructured) {
	ann := e.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[AnnotationFingerprint] = Fingerprint(e)
	e.SetAnnotations(ann)
}

// Same says live was last written with want's spec (want is Stamped).
func Same(live, want *unstructured.Unstructured) bool {
	f := live.GetAnnotations()[AnnotationFingerprint]
	return f != "" && f == want.GetAnnotations()[AnnotationFingerprint]
}

// State is how ESO sees an ExternalSecret.
type State int

const (
	// Pending: ESO has not judged the current spec yet.
	Pending State = iota
	// Synced: the target Secret holds what the current spec reads.
	Synced
	// Stale: the current spec synced once, but ESO's last refresh failed
	// (a store briefly unreachable): the Secret still holds the values it
	// read, which pods keep using.
	Stale
	// Failed: ESO could not read or write it for the current spec (the
	// store, the key or the template); it keeps retrying.
	Failed
)

// AnnotationWritten is on an ExternalSecret: when the agent last wrote its
// spec (RFC 3339). ESO's status says nothing of which spec a failure is
// about, so a failure is believed only Grace after the spec was written.
const AnnotationWritten = "odicea.dev/written-at"

// Grace is how long after writing a spec a failure may still be ESO's
// verdict on the previous one (ESO reconciles a changed spec at once).
const Grace = 20 * time.Second

// Written records on e that its spec is written at t.
func Written(e *unstructured.Unstructured, t time.Time) {
	ann := e.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[AnnotationWritten] = t.UTC().Format(time.RFC3339)
	e.SetAnnotations(ann)
}

// StateOf reads e's Ready condition at now. Ready counts only once ESO
// synced e's current generation (status.syncedResourceVersion is
// "<generation>-<hash>"); a failure after that is Stale; a failure before
// it is Failed once Grace has passed since the spec was written.
func StateOf(e *unstructured.Unstructured, now time.Time) State {
	synced, _, _ := unstructured.NestedString(e.Object, "status", "syncedResourceVersion")
	current := strings.HasPrefix(synced, strconv.FormatInt(e.GetGeneration(), 10)+"-")
	conds, _, _ := unstructured.NestedSlice(e.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != "Ready" {
			continue
		}
		switch {
		case m["status"] == "True" && current:
			return Synced
		case m["status"] == "False" && current:
			return Stale
		case m["status"] == "False":
			at, err := time.Parse(time.RFC3339, e.GetAnnotations()[AnnotationWritten])
			if err == nil && now.Sub(at) < Grace {
				return Pending
			}
			return Failed
		}
	}
	return Pending
}

// Literal is s as a constant of an ESO template (engineVersion v2, Go
// templates): a quoted Go string, so whatever it holds ("{{", quotes,
// newlines) is text and never runs.
func Literal(s string) string { return strconv.Quote(s) }

// Escaped is the template pipeline that writes x (a data reference like
// ".v0" or a Literal) escaped for a URL's user, password or path: every
// reserved character percent-encoded, a space as %20 (urlquery alone
// writes "+", which a URL's user info reads as a plus).
func Escaped(x string) string { return x + ` | urlquery | replace "+" "%20"` }

// Action is a template action that writes pipeline p.
func Action(p string) string { return "{{ " + p + " }}" }

// StoreRef points at the ClusterSecretStore named store (the agent reads
// only stores IT registered for the whole cluster).
func StoreRef(store string) map[string]any {
	return map[string]any{"kind": "ClusterSecretStore", "name": store}
}
