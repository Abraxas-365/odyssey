// Package secretstore keeps the managed secrets of the apps of this cluster
// (phase 2a). A person types a value in the console; the browser seals it to
// this cluster's key (pkg/secretbox) and the control plane carries it through
// the tunnel without being able to read it. The agent opens it here and
// writes it into the Secret <app>-secrets of the environment's namespace,
// where the app's pods read it as an environment variable.
//
// Versions make deliveries idempotent and ordered: the Secret remembers the
// version of each value (annotation odicea.dev/secret-versions) and only a
// higher one replaces it, so a late or repeated delivery changes nothing.
package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/Abraxas-365/odyssey/agent/desired"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The agent writes the apps' Secrets in the per-environment namespaces (it
// creates those itself). It reads Secrets only by name through the uncached
// API reader, so it never needs list or watch. Its access to them is the
// ClusterRole desired.SecretsRole, bound only in those namespaces (see
// desired.Namespaces and agent/install/rbac.yaml): not cluster-wide.

// Names and labels of what the agent keeps.
const (
	// AnnotationVersions holds a JSON map name → version of the values in a
	// Secret.
	AnnotationVersions = "odicea.dev/secret-versions"
	// AnnotationDelivered holds a JSON map name → RFC 3339 (UTC) time of the
	// last write of each value in a Secret. The app reconciler keeps a value
	// its spec does not list yet for a grace period after that time.
	AnnotationDelivered = "odicea.dev/secret-delivered"
	// LabelApp is the app a Secret belongs to (the same label the app
	// controller puts on its objects).
	LabelApp = "odyssey.dev/app"
)

// Name is the Secret that holds an app's managed secrets.
func Name(app string) string { return app + "-secrets" }

// Versions are the versions recorded in s (empty when it has none or the
// annotation is unreadable: then no stored value counts as current).
func Versions(s *corev1.Secret) map[string]int64 {
	out := map[string]int64{}
	if s == nil {
		return out
	}
	if raw := s.Annotations[AnnotationVersions]; raw != "" {
		var m map[string]int64
		if json.Unmarshal([]byte(raw), &m) == nil {
			out = m
		}
	}
	return out
}

// SetVersions records versions in s.
func SetVersions(s *corev1.Secret, versions map[string]int64) {
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	raw, _ := json.Marshal(versions) // a map of strings to numbers always marshals
	s.Annotations[AnnotationVersions] = string(raw)
}

// Delivered are the times recorded in s for when each value was written
// (empty when it has none or the annotation is unreadable; an entry that does
// not parse is left out). A key without a time counts as old.
func Delivered(s *corev1.Secret) map[string]time.Time {
	out := map[string]time.Time{}
	if s == nil {
		return out
	}
	var raw map[string]string
	if json.Unmarshal([]byte(s.Annotations[AnnotationDelivered]), &raw) != nil {
		return out
	}
	for k, v := range raw {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out[k] = t
		}
	}
	return out
}

// SetDelivered records the times in s (no entries: no annotation).
func SetDelivered(s *corev1.Secret, delivered map[string]time.Time) {
	if len(delivered) == 0 {
		delete(s.Annotations, AnnotationDelivered)
		return
	}
	raw := make(map[string]string, len(delivered))
	for k, t := range delivered {
		raw[k] = t.UTC().Format(time.RFC3339)
	}
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	b, _ := json.Marshal(raw) // a map of strings always marshals
	s.Annotations[AnnotationDelivered] = string(b)
}

// Ours says the agent manages s (it carries the managed-by label). A Secret
// with the name of an app's but without it is someone else's: the agent
// neither writes, adopts nor prunes it.
func Ours(s *corev1.Secret) bool {
	return s.Labels[desired.LabelManagedBy] == desired.ManagedBy
}

// Writer opens delivered secrets and stores them. Its Client should read
// Secrets from the API, not from a cache (the agent's does).
type Writer struct {
	Client client.Client
	// Namespaces creates the environment's namespace (nil: on Client, binding
	// nothing).
	Namespaces *desired.Namespaces
	// Now is the clock (tests); nil uses time.Now.
	Now func() time.Time
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Receive handles one delivery for the cluster named cluster, whose key is
// key: it checks the message, opens the value (bound to this cluster,
// environment, app, name and version) and stores it unless the cluster already
// holds that version or a newer one. Errors never carry the value.
func (w *Writer) Receive(ctx context.Context, key secretbox.Key, cluster string, s *fleetv1.Secret) error {
	if err := desired.ValidSecret(s); err != nil {
		return err
	}
	if s.GetKeyId() != key.ID() {
		return errors.New("sealed for another cluster key")
	}
	value, err := key.Open(secretbox.Target{
		Cluster: cluster, Environment: s.GetEnvironmentId(), App: s.GetApp(), Name: s.GetName(), Version: s.GetVersion(),
	}, s.GetSealed())
	if err != nil {
		return err
	}
	defer clear(value)
	return w.Put(ctx, Delivery{
		Organization: s.GetOrganizationId(), Project: s.GetProjectId(), Environment: s.GetEnvironmentId(),
		App: s.GetApp(), Name: s.GetName(), Version: s.GetVersion(),
	}, value)
}

// Delivery says where a value goes.
type Delivery struct {
	Organization, Project, Environment string
	App, Name                          string
	Version                            int64
}

// Put stores value as Name in the app's Secret unless the stored version is
// already at least Version (then it succeeds without writing). The
// environment's namespace is created when it is not there yet. value is
// copied: the caller may zero it. It refuses a Secret of that name the agent
// does not manage (no managed-by label): the error is the delivery's ack
// error, and says nothing of the value.
func (w *Writer) Put(ctx context.Context, d Delivery, value []byte) error {
	nss := w.Namespaces
	if nss == nil {
		nss = &desired.Namespaces{Client: w.Client}
	}
	if err := nss.Ensure(ctx, d.Organization, d.Project, d.Environment); err != nil {
		return err
	}
	ns, name := desired.Namespace(d.Environment), Name(d.App)
	return retry.OnError(retry.DefaultRetry, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}, func() error {
		at := w.now()
		var sec corev1.Secret
		err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &sec)
		if apierrors.IsNotFound(err) {
			sec = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
			stamp(&sec, d)
			sec.Data = map[string][]byte{d.Name: append([]byte(nil), value...)}
			SetVersions(&sec, map[string]int64{d.Name: d.Version})
			SetDelivered(&sec, map[string]time.Time{d.Name: at})
			return w.Client.Create(ctx, &sec)
		}
		if err != nil {
			return fmt.Errorf("read secret %s/%s: %w", ns, name, err)
		}
		if !Ours(&sec) {
			return fmt.Errorf("a Secret named %s already exists in %s and is not managed by Odyssey", name, ns)
		}
		versions := Versions(&sec)
		if _, have := sec.Data[d.Name]; have && versions[d.Name] >= d.Version {
			return nil // that version, or a newer one, is already here
		}
		stamp(&sec, d)
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data[d.Name] = append([]byte(nil), value...)
		versions = maps.Clone(versions)
		versions[d.Name] = d.Version
		SetVersions(&sec, versions)
		delivered := Delivered(&sec)
		delivered[d.Name] = at
		SetDelivered(&sec, delivered)
		return w.Client.Update(ctx, &sec)
	})
}

// stamp labels the Secret as the agent's, for d's app.
func stamp(sec *corev1.Secret, d Delivery) {
	if sec.Labels == nil {
		sec.Labels = map[string]string{}
	}
	sec.Labels[desired.LabelManagedBy] = desired.ManagedBy
	sec.Labels[LabelApp] = d.App
}
