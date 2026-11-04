// Package app is the agent's controller for OdysseyApp: it runs the app as a
// Deployment (rolling, the previous version serves until the new one answers
// its health check), a Service and an Ingress, and reports how the rollout of
// the spec's revision is going.
package app

import (
	"sort"
	"strconv"

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
}

func (c Config) host(a *v1alpha1.OdysseyApp) string { return a.Name + "." + c.Domain }

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
// Secrets; a secret replaces a plain variable of its name), plus PORT unless
// the spec sets it either way.
func env(a *v1alpha1.OdysseyApp) []corev1.EnvVar {
	secret := secretEnv(a)
	secretNames := make(map[string]bool, len(secret))
	for _, e := range secret {
		secretNames[e.Name] = true
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
	_, plain := a.Spec.Env["PORT"]
	if !plain && !secretNames["PORT"] {
		out = append(out, corev1.EnvVar{Name: "PORT", Value: strconv.Itoa(int(a.Spec.Port))})
	}
	return out
}

// mutateDeployment sets d to run a's spec revision. Every revision changes
// the pod template (its revision label), so every deployment rolls out.
func mutateDeployment(d *appsv1.Deployment, a *v1alpha1.OdysseyApp, cfg Config) {
	l := labels(a)
	pod := make(map[string]string, len(a.Spec.Labels)+3)
	for k, v := range a.Spec.Labels {
		pod[k] = v
	}
	// The agent's own labels last: the selector, the cache and the revision
	// depend on them.
	pod[LabelManagedBy], pod[LabelApp], pod[LabelRevision] = ManagedBy, a.Name, strconv.FormatInt(a.Spec.Revision, 10)
	replicas, zero, one := a.Spec.Replicas, intstr.FromInt32(0), intstr.FromInt32(1)
	no := false
	deadline := cfg.ReadyDeadline
	d.Labels = l
	d.Spec.Replicas = &replicas
	// Only the app label: the selector of a Deployment is immutable.
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{LabelApp: a.Name}}
	d.Spec.Strategy = appsv1.DeploymentStrategy{
		Type:          appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one},
	}
	d.Spec.ProgressDeadlineSeconds = &deadline
	d.Spec.RevisionHistoryLimit = ptr(int32(3))
	d.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: pod, Annotations: podAnnotations(a)},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no,
			EnableServiceLinks:           &no,
			SecurityContext:              &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name:            "app",
				Image:           a.Spec.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: a.Spec.Port, Protocol: corev1.ProtocolTCP}},
				Env:             env(a),
				// Readiness only: a version is "ready" when its health check
				// passes. Liveness (restarts) is per-app configuration, later.
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: a.Spec.HealthPath, Port: intstr.FromString("http")}},
					PeriodSeconds:    3,
					TimeoutSeconds:   2,
					SuccessThreshold: 1,
					FailureThreshold: 2,
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			}},
		},
	}
}

// podAnnotations roll the pods when the app's secrets change (none: apps
// without secrets keep the template they had).
func podAnnotations(a *v1alpha1.OdysseyApp) map[string]string {
	if h := secretsHash(a); h != "" {
		return map[string]string{AnnotationSecrets: h}
	}
	return nil
}

func mutateService(s *corev1.Service, a *v1alpha1.OdysseyApp) {
	s.Labels = labels(a)
	s.Spec.Type = corev1.ServiceTypeClusterIP
	s.Spec.Selector = map[string]string{LabelApp: a.Name}
	s.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}}
}

func mutateIngress(i *networkingv1.Ingress, a *v1alpha1.OdysseyApp, cfg Config) {
	prefix := networkingv1.PathTypePrefix
	i.Labels = labels(a)
	if cfg.IngressClass != "" {
		i.Spec.IngressClassName = &cfg.IngressClass
	}
	i.Spec.Rules = []networkingv1.IngressRule{{
		Host: cfg.host(a),
		IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
			Path: "/", PathType: &prefix,
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: a.Name, Port: networkingv1.ServiceBackendPort{Name: "http"},
			}},
		}}}},
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
