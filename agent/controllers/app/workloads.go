package app

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// An app runs as one workload per service (MS-2): a Deployment selected by
// LabelWorkload, plus, for a web service, a Service the app's other services
// reach it by and, for a public one (a route), an Ingress on the app's host.
// LabelApp stays on everything as membership (egress policies, secrets,
// pods → app), never as a selector.
const (
	// LabelWorkload selects a service's pods: its Deployment's name.
	LabelWorkload = "odyssey.dev/workload"
	// LabelService is the service's name inside its app.
	LabelService = "odyssey.dev/service"
)

// workload is one service of an app as the agent runs it.
type workload struct {
	v1alpha1.ServiceSpec
	// Deployment is the name of its Deployment and the value of its
	// LabelWorkload: "<app>-<service>".
	Deployment string
	// Object names its Service and Ingress: "<app>" for the service at "/"
	// (the app's host and in-cluster address do not change), Deployment for
	// any other.
	Object string
}

func (w workload) web() bool { return w.Kind == v1alpha1.ServiceWeb }

// public: it answers on the app's address.
func (w workload) public() bool { return w.web() && w.Route != "" }

// root: it answers the whole address.
func (w workload) root() bool { return w.web() && w.Route == "/" }

// workloads are a's services in order. An app without services (an object
// written before them) is one web service "web" at "/" made of the
// top-level fields.
func workloads(a *v1alpha1.OdysseyApp) []workload {
	specs := a.Spec.Services
	if len(specs) == 0 {
		specs = []v1alpha1.ServiceSpec{{Name: "web", Kind: v1alpha1.ServiceWeb, Route: "/", Image: a.Spec.Image,
			Port: a.Spec.Port, HealthPath: a.Spec.HealthPath, Replicas: a.Spec.Replicas}}
	}
	out := make([]workload, len(specs))
	for i, s := range specs {
		if s.Kind == "" {
			s.Kind = v1alpha1.ServiceWeb
		}
		w := workload{ServiceSpec: s, Deployment: objectName(a.Name, s.Name)}
		w.Object = w.Deployment
		if w.root() {
			w.Object = a.Name
		}
		out[i] = w
	}
	return out
}

// objectName is "<app>-<service>", kept a DNS label (63): a long app name
// is cut and a hash of the whole keeps it unique.
func objectName(app, service string) string {
	name := app + "-" + service
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	h := hex.EncodeToString(sum[:])[:8]
	keep := 63 - len(service) - len(h) - 2
	return strings.TrimRight(app[:keep], "-") + "-" + h + "-" + service
}

// serviceEnvPrefix starts the variables that tell a service where its web
// siblings answer inside the app (ODICEA_SERVICE_<NAME>_URL).
const serviceEnvPrefix = "ODICEA_SERVICE_"

// withServiceEnv adds ODICEA_SERVICE_<NAME>_URL=http://<object> for every
// web service of the app other than w (port 80 of its Service). They win
// over a variable of the same name.
func withServiceEnv(in []corev1.EnvVar, w workload, all []workload) []corev1.EnvVar {
	var add []corev1.EnvVar
	for _, s := range all {
		if s.Name != w.Name && s.web() {
			add = append(add, corev1.EnvVar{Name: serviceEnvPrefix + strings.ToUpper(s.Name) + "_URL", Value: "http://" + s.Object})
		}
	}
	ours := make(map[string]bool, len(add))
	for _, e := range add {
		ours[e.Name] = true
	}
	out := make([]corev1.EnvVar, 0, len(in)+len(add))
	for _, e := range in {
		if !ours[e.Name] && !strings.HasPrefix(e.Name, serviceEnvPrefix) {
			out = append(out, e)
		}
	}
	return append(out, add...)
}
