package app

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	networkingv1 "k8s.io/api/networking/v1"
)

// UnavailableMessage is what an app asking for sign-in protection reports in
// a cluster that cannot provide it (reason config). The app is not exposed.
const UnavailableMessage = "Este clúster no puede proteger apps con inicio de sesión. Pide a TI que lo habilite."

// LabelEnvironment is the environment id the agent puts on every OdysseyApp
// (agent/desired); the gate needs it to bind its cookies to the environment.
const LabelEnvironment = "odyssey.dev/environment"

// Policy is what the sign-in gate must enforce for one app.
type Policy struct {
	Mode         v1alpha1.AccessMode // team or organization (public needs no gate)
	Environment  string              // the app's environment id
	Organization string              // the app's organization id
	// Host, Scheme and Port say where the app is served (from the agent's
	// config, not from requests): the gate refuses a request whose host is
	// not Host, and builds the sign-in return URL from them. Port 0 is the
	// scheme's default.
	Host   string
	Scheme string
	Port   int
}

// Exposure is the port to the ingress variant that puts the sign-in gate in
// front of an app. Traefik is the one adapter today; Gateway API or nginx
// would be others. Implementations must be idempotent.
type Exposure interface {
	// Available reports whether the cluster can protect apps now (the
	// variant's API exists). A protected app is never exposed otherwise.
	Available(ctx context.Context) bool
	// Protect creates or updates, owned by a, what the gate needs for a.
	Protect(ctx context.Context, a *v1alpha1.OdysseyApp, p Policy) error
	// IngressAnnotations are the annotations that make a's Ingress use
	// what Protect created. Public apps have none of these keys.
	IngressAnnotations(a *v1alpha1.OdysseyApp) map[string]string
	// Unprotect removes what Protect created (absent is fine).
	Unprotect(ctx context.Context, a *v1alpha1.OdysseyApp) error
}

// accessMode is a's mode; an app without access is public (an object
// written before access existed).
func accessMode(a *v1alpha1.OdysseyApp) v1alpha1.AccessMode {
	if a.Spec.Access == nil || a.Spec.Access.Mode == "" {
		return v1alpha1.AccessPublic
	}
	return a.Spec.Access.Mode
}

func policyOf(a *v1alpha1.OdysseyApp, cfg Config) Policy {
	env := a.Labels[LabelEnvironment]
	if env == "" {
		env = strings.TrimPrefix(a.Namespace, "odyssey-")
	}
	p := Policy{Mode: accessMode(a), Environment: env, Host: cfg.host(a), Scheme: cfg.URLScheme, Port: cfg.URLPort}
	if a.Spec.Access != nil {
		p.Organization = a.Spec.Access.Organization
	}
	return p
}

// verifyAddress is the gate's /verify URL for p.
func verifyAddress(base string, p Policy) string {
	q := url.Values{"env": {p.Environment}, "org": {p.Organization}, "mode": {string(p.Mode)}, "host": {strings.ToLower(p.Host)}, "scheme": {p.Scheme}}
	if p.Port != 0 {
		q.Set("port", strconv.Itoa(p.Port))
	}
	return base + "?" + q.Encode()
}

// applyExposure sets (protected) or clears (public) the exposure's
// annotations on the Ingress. A public app never keeps the middleware
// annotation, with or without an Exposure configured: it would point Traefik
// at Middlewares that are gone (404).
func applyExposure(i *networkingv1.Ingress, x Exposure, a *v1alpha1.OdysseyApp, protected bool) {
	if !protected {
		delete(i.Annotations, annotationMiddlewares)
	}
	if x == nil {
		return
	}
	for k, v := range x.IngressAnnotations(a) {
		if protected {
			if i.Annotations == nil {
				i.Annotations = map[string]string{}
			}
			i.Annotations[k] = v
		} else {
			delete(i.Annotations, k)
		}
	}
}
