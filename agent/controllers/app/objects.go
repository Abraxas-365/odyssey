// Package app is the agent's controller for OdysseyApp: it runs the app as a
// Deployment (rolling, the previous version serves until the new one answers
// its health check), a Service and an Ingress, and reports how the rollout of
// the spec's revision is going.
package app

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Abraxas-365/odyssey/agent/access"
	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// LabelManagedBy marks every object the agent owns (its cache sees only these).
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedBy      = "odyssey-agent"
	// LabelApp selects the app's pods (the Deployment's selector).
	LabelApp = "odyssey.dev/app"
	// LabelAI is on the pods of an app that uses AI: the egress fence opens the
	// AI gateway to them (agent/egress.LabelAI, policy odicea-ai).
	LabelAI = "odyssey.dev/ai"
	// LabelRevision is the spec revision a pod runs.
	LabelRevision = "odyssey.dev/revision"
)

// Config is how the agent exposes apps.
type Config struct {
	// Domain is the base of the apps' hosts: <app>.<Domain>.
	Domain string
	// URLScheme and URLPort build the reported URL (the ingress may sit
	// behind a port mapping, as in k3d). URLPort 0 omits the port.
	URLScheme string
	URLPort   int
	// IngressClass is the ingress class to use ("" = the cluster default).
	IngressClass string
	// ReadyDeadline is how long a rollout may go without progress before the
	// app is reported failed (its health check never passed).
	ReadyDeadline int32
	// AuthJWKSURL is the gate's /jwks as the apps reach it
	// (ODICEA_AUTH_JWKS_URL); empty: DefaultAuthJWKSURL.
	AuthJWKSURL string
}

// DefaultAuthJWKSURL is where protected apps fetch the identity assertion keys.
const DefaultAuthJWKSURL = "http://odyssey-access.odyssey-system.svc:8099/jwks"

func (c Config) host(a *v1alpha1.OdysseyApp) string { return a.Name + "." + c.Domain }

// drainSeconds is how long a web pod being replaced keeps answering
// (preStop) before it is told to stop.
const drainSeconds = 5

// URL is where app a answers.
func (c Config) URL(a *v1alpha1.OdysseyApp) string {
	u := c.URLScheme + "://" + c.host(a)
	if c.URLPort != 0 {
		u += ":" + strconv.Itoa(c.URLPort)
	}
	return u
}

// labels are the spec's labels plus the agent's own.
func labels(a *v1alpha1.OdysseyApp) map[string]string {
	l := make(map[string]string, len(a.Spec.Labels)+2)
	for k, v := range a.Spec.Labels {
		l[k] = v
	}
	l[LabelManagedBy] = ManagedBy
	l[LabelApp] = a.Name
	return l
}

// env is the spec's env, sorted, then the secret variables (read from
// Secrets; a secret replaces a plain variable of its name), then the
// resources' (they win over both), plus PORT unless the spec sets it either
// way (the top-level port: an app without services).
func env(a *v1alpha1.OdysseyApp) []corev1.EnvVar { return envFor(a, a.Spec.Port) }

// envFor is env for a service listening on port (0: a worker, no PORT).
func envFor(a *v1alpha1.OdysseyApp, port int32) []corev1.EnvVar {
	res := resourceEnv(a)
	resNames := resourceNames(a)
	var secret []corev1.EnvVar
	for _, e := range secretEnv(a) {
		if !resNames[e.Name] {
			secret = append(secret, e)
		}
	}
	secretNames := make(map[string]bool, len(secret)+len(res))
	for _, e := range secret {
		secretNames[e.Name] = true
	}
	for n := range resNames {
		secretNames[n] = true
	}
	keys := make([]string, 0, len(a.Spec.Env))
	for k := range a.Spec.Env {
		if !secretNames[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(keys)+len(secret)+1)
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: a.Spec.Env[k]})
	}
	out = append(out, secret...)
	out = append(out, res...)
	_, plain := a.Spec.Env["PORT"]
	if !plain && !secretNames["PORT"] && port != 0 {
		out = append(out, corev1.EnvVar{Name: "PORT", Value: strconv.Itoa(int(port))})
	}
	return out
}

// withAuthEnv adds, to a protected app (team/organization; never a public
// one), the variables pkg/odiceaauth reads to verify the gate's identity
// assertion. They win over everything: a variable of the same name is dropped
// first. ODICEA_AUTH_AUDIENCE is the gate's aud for this app, host[:port] as
// the browser sends it.
func withAuthEnv(in []corev1.EnvVar, a *v1alpha1.OdysseyApp, cfg Config) []corev1.EnvVar {
	// A public app gets none, and keeps none it was handed (the control plane
	// reserves ODICEA_, so only a hand-edited OdysseyApp could carry them).
	if accessMode(a) == v1alpha1.AccessPublic {
		out := make([]corev1.EnvVar, 0, len(in))
		for _, e := range in {
			if !strings.HasPrefix(e.Name, "ODICEA_AUTH_") {
				out = append(out, e)
			}
		}
		return out
	}
	aud := strings.ToLower(cfg.host(a))
	if cfg.URLPort != 0 {
		aud += ":" + strconv.Itoa(cfg.URLPort)
	}
	jwks := cfg.AuthJWKSURL
	if jwks == "" {
		jwks = DefaultAuthJWKSURL
	}
	auth := []corev1.EnvVar{
		{Name: "ODICEA_AUTH_JWKS_URL", Value: jwks},
		{Name: "ODICEA_AUTH_ISSUER", Value: access.AssertionIssuer},
		{Name: "ODICEA_AUTH_AUDIENCE", Value: aud},
		{Name: "ODICEA_AUTH_HEADER", Value: access.AssertionHeader},
	}
	ours := make(map[string]bool, len(auth))
	for _, e := range auth {
		ours[e.Name] = true
	}
	out := make([]corev1.EnvVar, 0, len(in)+len(auth))
	for _, e := range in {
		if !ours[e.Name] {
			out = append(out, e)
		}
	}
	return append(out, auth...)
}

// mutateDeployment sets d to run a's first service (tests; an app without
// services has exactly that one).
func mutateDeployment(d *appsv1.Deployment, a *v1alpha1.OdysseyApp, cfg Config) {
	ws := workloads(a)
	mutateWorkload(d, a, ws[0], ws, cfg)
}

// mutateWorkload sets d to run service w of a's spec revision. Every
// revision changes the pod template (its revision label), so every
// deployment rolls out.
func mutateWorkload(d *appsv1.Deployment, a *v1alpha1.OdysseyApp, w workload, all []workload, cfg Config) {
	l := labels(a)
	l[LabelWorkload], l[LabelService] = w.Deployment, w.Name
	pod := make(map[string]string, len(a.Spec.Labels)+5)
	for k, v := range a.Spec.Labels {
		pod[k] = v
	}
	// The agent's own labels last: the selector, the cache and the revision
	// depend on them.
	pod[LabelManagedBy], pod[LabelApp], pod[LabelRevision] = ManagedBy, a.Name, strconv.FormatInt(a.Spec.Revision, 10)
	pod[LabelWorkload], pod[LabelService] = w.Deployment, w.Name
	delete(pod, LabelAI) // never a person's: only spec.ai sets it
	if a.Spec.AI != nil {
		pod[LabelAI] = "true"
	}
	replicas, zero, one := w.Replicas, intstr.FromInt32(0), intstr.FromInt32(1)
	no := false
	deadline := cfg.ReadyDeadline
	d.Labels = l
	d.Spec.Replicas = &replicas
	// Only the workload label: the selector of a Deployment is immutable,
	// and the app's label is on every service's pods.
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{LabelWorkload: w.Deployment}}
	d.Spec.Strategy = appsv1.DeploymentStrategy{
		Type:          appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one},
	}
	if hasFiles(a) {
		// A ReadWriteOnce volume cannot be mounted by the old and the new pod
		// at once: the old one goes first (a short outage, by design).
		d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	d.Spec.ProgressDeadlineSeconds = &deadline
	d.Spec.RevisionHistoryLimit = ptr(int32(3))
	vols, mounts := resourceVolumes(a)
	podSecurity := &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	if len(vols) > 0 {
		group, onMismatch := filesGroup, corev1.FSGroupChangeOnRootMismatch
		podSecurity.FSGroup, podSecurity.FSGroupChangePolicy = &group, &onMismatch
	}
	container := corev1.Container{
		Name:            "app",
		Image:           w.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             withServiceEnv(withAuthEnv(withAIEnv(envFor(a, w.Port), a), a, cfg), w, all),
		VolumeMounts:    mounts,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &no,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	}
	if w.web() {
		container.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: w.Port, Protocol: corev1.ProtocolTCP}}
		// Readiness only: a version is "ready" when its health check
		// passes. Liveness (restarts) is per-app configuration, later. A
		// worker has none: it is ready once its process runs.
		container.ReadinessProbe = &corev1.Probe{
			ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: w.HealthPath, Port: intstr.FromString("http")}},
			PeriodSeconds:    3,
			TimeoutSeconds:   2,
			SuccessThreshold: 1,
			FailureThreshold: 2,
		}
		// A pod being replaced keeps answering for a moment: the ingress
		// learns that it left a little after the Service does, and a pod that
		// stopped at once would fail those requests (seen on k3d with
		// Traefik). The sleep needs no shell in the image.
		container.Lifecycle = &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Sleep: &corev1.SleepAction{Seconds: drainSeconds}}}
	}
	d.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: pod, Annotations: podAnnotations(a)},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no,
			EnableServiceLinks:           &no,
			SecurityContext:              podSecurity,
			Volumes:                      vols,
			Containers:                   []corev1.Container{container},
		},
	}
}

// podAnnotations roll the pods when the app's secrets or resources change
// (none: apps without either keep the template they had).
func podAnnotations(a *v1alpha1.OdysseyApp) map[string]string {
	var out map[string]string
	if h := secretsHash(a); h != "" {
		out = map[string]string{AnnotationSecrets: h}
	}
	if h := resourcesHash(a); h != "" {
		if out == nil {
			out = map[string]string{}
		}
		out[AnnotationResources] = h
	}
	return out
}

// mutateService sets s to reach web service w of a. During the hot
// migration of an app from before services (legacy: its Deployment named
// after the app still runs and w is the app's only web service) it keeps
// selecting by the app's label, which the old and the new pods both carry,
// so the address never goes without a ready pod; the selector narrows to
// the workload once the old Deployment is gone.
func mutateService(s *corev1.Service, a *v1alpha1.OdysseyApp, w workload, legacy bool) {
	s.Labels = labels(a)
	s.Labels[LabelWorkload], s.Labels[LabelService] = w.Deployment, w.Name
	s.Spec.Type = corev1.ServiceTypeClusterIP
	s.Spec.Selector = map[string]string{LabelWorkload: w.Deployment}
	if legacy {
		s.Spec.Selector = map[string]string{LabelApp: a.Name}
	}
	s.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}}
}

// mutateIngress sets i to route the app's address (all of it) to its
// first service (tests: an app without services).
func mutateIngress(i *networkingv1.Ingress, a *v1alpha1.OdysseyApp, cfg Config) {
	mutateRoute(i, a, workloads(a)[0], false, cfg)
}

// mutateRoute sets i to send w's route of the app's host to w. "/" is a
// Prefix of everything; "/api" is the exact path plus the Prefix "/api/",
// so "/apix" never reaches it whatever prefix matching the ingress does
// (Traefik's is by text). Which of the app's Ingresses wins a request is
// the ingress' own ranking: for Traefik, IngressAnnotations ranks every
// route other than "/" above the root, the longer ahead. callback adds the
// sign-in callback (/_odicea/callback) when no service answers "/": the
// gate must see it.
func mutateRoute(i *networkingv1.Ingress, a *v1alpha1.OdysseyApp, w workload, callback bool, cfg Config) {
	prefix, exact := networkingv1.PathTypePrefix, networkingv1.PathTypeExact
	i.Labels = labels(a)
	i.Labels[LabelWorkload], i.Labels[LabelService] = w.Deployment, w.Name
	if cfg.IngressClass != "" {
		i.Spec.IngressClassName = &cfg.IngressClass
	} else {
		i.Spec.IngressClassName = nil
	}
	backend := networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
		Name: w.Object, Port: networkingv1.ServiceBackendPort{Name: "http"},
	}}
	var paths []networkingv1.HTTPIngressPath
	if w.Route == "/" {
		paths = []networkingv1.HTTPIngressPath{{Path: "/", PathType: &prefix, Backend: backend}}
	} else {
		paths = []networkingv1.HTTPIngressPath{
			{Path: w.Route, PathType: &exact, Backend: backend},
			{Path: w.Route + "/", PathType: &prefix, Backend: backend},
		}
	}
	if callback {
		paths = append(paths, networkingv1.HTTPIngressPath{Path: access.CallbackPath, PathType: &exact, Backend: backend})
	}
	i.Spec.Rules = []networkingv1.IngressRule{{
		Host:             cfg.host(a),
		IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}},
	}}
}

func ptr[T any](v T) *T { return &v }

// revisionOf is the spec revision a Deployment's pod template runs (0 if none).
func revisionOf(d *appsv1.Deployment) int64 {
	n, err := strconv.ParseInt(d.Spec.Template.Labels[LabelRevision], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
