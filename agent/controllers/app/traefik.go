package app

import (
	"context"
	"fmt"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// The agent manages Traefik's Middlewares as unstructured objects (the
// Traefik Go module is not a dependency). Reading them goes to the API, not
// the cache.
// The cookie key's Secret is created by the agent (agent.ensureAccessKey):
// the gate then needs only to read it.
// +kubebuilder:rbac:groups="",namespace=odyssey-system,resources=secrets,verbs=create
// +kubebuilder:rbac:groups=traefik.io,resources=middlewares,verbs=get;list;watch;create;update;patch;delete

// Identity headers the gate sets after a successful sign-in. The strip
// middleware empties them on the way in so an app can trust them only when
// they come from the gate (Traefik removes a request header set to "").
var identityHeaders = []string{"X-Odicea-User", "X-Odicea-Email", "X-Odicea-Name", "X-Odicea-Org"}

// stripHeaders are emptied on the way in: the identity headers and their
// underscore spellings (some frameworks and CGI-style servers map "_" to
// "-"). The session cookie (__odicea_access) is not removed: Cookie is one
// header and Traefik's headers middleware cannot edit a part of it. It
// reaches the app, but it is an HMAC-signed value bound to the app's host,
// environment, mode and organization: useless anywhere else.
func stripHeaders() []string {
	out := append([]string(nil), identityHeaders...)
	for _, h := range identityHeaders {
		out = append(out, strings.ReplaceAll(h, "-", "_"))
	}
	return out
}

// NewMiddleware is an empty traefik.io Middleware (to watch or read).
func NewMiddleware() *unstructured.Unstructured {
	m := &unstructured.Unstructured{}
	m.SetGroupVersionKind(middlewareGVK)
	return m
}

var middlewareGVK = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"}

// Traefik protects apps with two Middlewares of the app's namespace:
// <app>-odicea-strip (drops spoofed identity headers) and
// <app>-odicea-access (forwardAuth to the gate), chained in the Ingress'
// router.middlewares annotation.
type Traefik struct {
	Client client.Client
	// VerifyURL is the gate's /verify endpoint as Traefik reaches it
	// (http://odyssey-access.odyssey-system.svc:8098/verify).
	VerifyURL string
	// Mapper tells whether the Middleware kind exists (nil: Client's).
	Mapper meta.RESTMapper
}

var _ Exposure = (*Traefik)(nil)

const annotationMiddlewares = "traefik.ingress.kubernetes.io/router.middlewares"

func stripName(a *v1alpha1.OdysseyApp) string  { return a.Name + "-odicea-strip" }
func accessName(a *v1alpha1.OdysseyApp) string { return a.Name + "-odicea-access" }

func (t *Traefik) Available(context.Context) bool {
	m := t.Mapper
	if m == nil {
		m = t.Client.RESTMapper()
	}
	_, err := m.RESTMapping(middlewareGVK.GroupKind(), middlewareGVK.Version)
	return err == nil
}

func (t *Traefik) IngressAnnotations(a *v1alpha1.OdysseyApp) map[string]string {
	ref := func(name string) string { return a.Namespace + "-" + name + "@kubernetescrd" }
	// The strip runs first: the gate's headers are added after it.
	return map[string]string{annotationMiddlewares: ref(stripName(a)) + "," + ref(accessName(a))}
}

func (t *Traefik) Protect(ctx context.Context, a *v1alpha1.OdysseyApp, p Policy) error {
	strip := map[string]any{}
	for _, h := range stripHeaders() {
		strip[h] = ""
	}
	auth := make([]any, len(identityHeaders))
	for i, h := range identityHeaders {
		auth[i] = h
	}
	for _, m := range []struct {
		name string
		spec map[string]any
	}{
		{stripName(a), map[string]any{"headers": map[string]any{"customRequestHeaders": strip}}},
		{accessName(a), map[string]any{"forwardAuth": map[string]any{
			"address": verifyAddress(t.VerifyURL, p),
			// false: Traefik sets X-Forwarded-Host/-Uri/-Method itself from
			// the real request, so a client cannot choose the host the gate
			// sees. (The gate also checks it against the host in "address".)
			"trustForwardHeader":  false,
			"authResponseHeaders": auth,
		}}},
	} {
		if err := t.put(ctx, a, m.name, m.spec); err != nil {
			return fmt.Errorf("middleware %s: %w", m.name, err)
		}
	}
	return nil
}

func (t *Traefik) put(ctx context.Context, a *v1alpha1.OdysseyApp, name string, spec map[string]any) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(middlewareGVK)
	obj.SetName(name)
	obj.SetNamespace(a.Namespace)
	_, err := controllerutil.CreateOrUpdate(ctx, t.Client, obj, func() error {
		obj.SetLabels(labels(a))
		obj.Object["spec"] = spec
		return controllerutil.SetControllerReference(a, obj, t.Client.Scheme())
	})
	return err
}

func (t *Traefik) Unprotect(ctx context.Context, a *v1alpha1.OdysseyApp) error {
	if !t.Available(ctx) {
		return nil // nothing of ours can exist without the kind
	}
	for _, name := range []string{stripName(a), accessName(a)} {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(middlewareGVK)
		// Look first: a public app's every pass would otherwise send a DELETE.
		if err := t.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("read middleware %s: %w", name, err)
		}
		if err := t.Client.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete middleware %s: %w", name, err)
		}
	}
	return nil
}
