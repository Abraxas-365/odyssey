package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/agent/access"
	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// The agent manages Traefik's Middlewares as unstructured objects (the
// Traefik Go module is not a dependency). Reading them goes to the API, not
// the cache.
// The cookie key's and the assertion key's Secrets (odyssey-access,
// odyssey-access-signing) are created by the agent (agent.ensureAccessKey):
// the gate then needs only to read them.
// +kubebuilder:rbac:groups="",namespace=odyssey-system,resources=secrets,verbs=create
// +kubebuilder:rbac:groups=traefik.io,resources=middlewares,verbs=get;list;watch;create;update;patch;delete

// Identity headers the gate sets after a successful sign-in. The strip
// middleware empties them on the way in so an app can trust them only when
// they come from the gate (Traefik removes a request header set to "").
var identityHeaders = []string{"X-Odicea-User", "X-Odicea-Email", "X-Odicea-Name", "X-Odicea-Org", access.AssertionHeader}

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
	// (http://odyssey-access.odyssey-system.svc:8098/verify). Empty: the
	// gate is not installed; routes' prefixes are still removed, but no app
	// can be protected.
	VerifyURL string
	// Mapper tells whether the Middleware kind exists (nil: Client's).
	Mapper meta.RESTMapper
}

var _ Exposure = (*Traefik)(nil)

const annotationMiddlewares = "traefik.ingress.kubernetes.io/router.middlewares"

// annotationPriority ranks a route's routers. Traefik ranks by the rule's
// length by default, and Host(h) && Path(`/api`) is shorter than the root's
// Host(h) && PathPrefix(`/`): the web would take "/api". A route other than
// "/" ranks at routePriority plus its length, above any root (whose
// default is under 300: a host has at most 253 characters) and with the
// longer route ahead ("/api/v2" before "/api/").
const (
	annotationPriority = "traefik.ingress.kubernetes.io/router.priority"
	routePriority      = 1000
)

func stripName(a *v1alpha1.OdysseyApp) string  { return a.Name + "-odicea-strip" }
func accessName(a *v1alpha1.OdysseyApp) string { return a.Name + "-odicea-access" }

// prefixName is the Middleware that removes a workload's route.
func prefixName(workload string) string { return workload + "-odicea-prefix" }

// labelMiddleware tells the agent's Middlewares apart by role ("prefix" for
// the route removers, which are listed to be collected).
const labelMiddleware = "odyssey.dev/middleware"

func (t *Traefik) Available(context.Context) bool {
	m := t.Mapper
	if m == nil {
		m = t.Client.RESTMapper()
	}
	_, err := m.RESTMapping(middlewareGVK.GroupKind(), middlewareGVK.Version)
	return err == nil
}

func (t *Traefik) CanProtect(ctx context.Context) bool { return t.VerifyURL != "" && t.Available(ctx) }

func (t *Traefik) IngressAnnotations(a *v1alpha1.OdysseyApp, w workload, protected bool) map[string]string {
	ref := func(name string) string { return a.Namespace + "-" + name + "@kubernetescrd" }
	var chain []string
	if protected {
		// The strip runs first: the gate's headers are added after it.
		chain = append(chain, ref(stripName(a)), ref(accessName(a)))
	}
	if w.StripPrefix {
		// Last: the gate judges, and returns the person to, the address
		// they opened, not the one the service sees.
		chain = append(chain, ref(prefixName(w.Deployment)))
	}
	priority := ""
	if w.Route != "/" {
		priority = strconv.Itoa(routePriority + len(w.Route))
	}
	return map[string]string{annotationMiddlewares: strings.Join(chain, ","), annotationPriority: priority}
}

func (t *Traefik) Prefixes(ctx context.Context, a *v1alpha1.OdysseyApp, want map[string]string) error {
	for workload, route := range want {
		spec := map[string]any{"stripPrefix": map[string]any{"prefixes": []any{route}}}
		if err := t.put(ctx, a, prefixName(workload), spec, "prefix"); err != nil {
			return fmt.Errorf("middleware %s: %w", prefixName(workload), err)
		}
	}
	return nil
}

func (t *Traefik) DropPrefixes(ctx context.Context, a *v1alpha1.OdysseyApp, keep map[string]string) error {
	if !t.Available(ctx) {
		return nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(middlewareGVK.GroupVersion().WithKind("MiddlewareList"))
	if err := t.Client.List(ctx, list, client.InNamespace(a.Namespace),
		client.MatchingLabels{LabelManagedBy: ManagedBy, LabelApp: a.Name, labelMiddleware: "prefix"}); err != nil {
		return fmt.Errorf("list middlewares: %w", err)
	}
	wanted := make(map[string]bool, len(keep))
	for workload := range keep {
		wanted[prefixName(workload)] = true
	}
	for i := range list.Items {
		m := &list.Items[i]
		if wanted[m.GetName()] || !metav1.IsControlledBy(m, a) {
			continue
		}
		if err := t.Client.Delete(ctx, m); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete middleware %s: %w", m.GetName(), err)
		}
	}
	return nil
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
		if err := t.put(ctx, a, m.name, m.spec, "access"); err != nil {
			return fmt.Errorf("middleware %s: %w", m.name, err)
		}
	}
	return nil
}

func (t *Traefik) put(ctx context.Context, a *v1alpha1.OdysseyApp, name string, spec map[string]any, role string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(middlewareGVK)
	obj.SetName(name)
	obj.SetNamespace(a.Namespace)
	_, err := controllerutil.CreateOrUpdate(ctx, t.Client, obj, func() error {
		l := labels(a)
		l[labelMiddleware] = role
		obj.SetLabels(l)
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
