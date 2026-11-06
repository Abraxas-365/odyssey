package app

import (
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// crashRestarts is how many restarts of the new version count as crashing
// even before Kubernetes says CrashLoopBackOff.
const crashRestarts = 3

var pullFailures = map[string]bool{"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true, "ErrImageNeverPull": true}

// Observation is the phase of revision rev, from its Deployment and the pods
// of that revision (older pods are the previous version still serving).
type Observation struct {
	Phase         v1alpha1.Phase
	Reason        v1alpha1.Reason
	Message       string
	ReadyReplicas int32
	// Services is each service's part (an app with spec.services only).
	Services []v1alpha1.ServiceStatus
}

// Observe decides how the rollout of revision rev is going at now. It judges
// the rollout only; what happens to the app after it is ready is not its
// concern (the reconciler keeps a settled verdict).
//
//   - ready: the Deployment saw the template, every replica is updated and
//     available, and none of the old ones remain.
//   - failed: a pod of rev cannot pull its image (fast), keeps crashing, has
//     been running longer than healthGrace without passing its health check,
//     or the rollout stopped progressing while every new pod was running.
//   - progressing: anything else (including a slow image download: the
//     control plane's own timeout bounds it).
func Observe(d *appsv1.Deployment, pods []corev1.Pod, rev int64, now time.Time, healthGrace time.Duration) Observation {
	o := Observation{Phase: v1alpha1.PhaseProgressing}
	if revisionOf(d) != rev {
		o.Message = "applying the new revision"
		return o
	}
	starting := false
	for i := range pods {
		p := &pods[i]
		if revisionLabel(p) != rev || p.DeletionTimestamp != nil {
			continue
		}
		if podReady(p) {
			o.ReadyReplicas++
		}
		for _, c := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			if w := c.State.Waiting; w != nil && pullFailures[w.Reason] {
				return failed(o, v1alpha1.ReasonImagePull, firstLine(w.Message, w.Reason))
			}
			if w := c.State.Waiting; (w != nil && w.Reason == "CrashLoopBackOff") || c.RestartCount >= crashRestarts {
				return failed(o, v1alpha1.ReasonCrash, crashMessage(c))
			}
		}
		if len(p.Status.ContainerStatuses) == 0 {
			starting = true // not even scheduled/created yet
		}
		for _, c := range p.Status.ContainerStatuses {
			if r := c.State.Running; r != nil && !c.Ready && healthGrace > 0 && now.Sub(r.StartedAt.Time) > healthGrace {
				return failed(o, v1alpha1.ReasonUnhealthy, fmt.Sprintf("the health check did not pass within %s", healthGrace))
			}
			if c.State.Running == nil && c.State.Terminated == nil {
				starting = true // ContainerCreating: usually downloading the image
			}
		}
	}
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	if s.ObservedGeneration >= d.Generation && s.UpdatedReplicas == want && s.AvailableReplicas == want && s.Replicas == want {
		o.Phase = v1alpha1.PhaseReady
		o.Message = ""
		return o
	}
	if starting {
		o.Message = "starting the new revision (downloading the image)"
		return o
	}
	if s.ObservedGeneration >= d.Generation {
		for _, c := range s.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
				return failed(o, v1alpha1.ReasonUnhealthy, "the health check never passed")
			}
		}
	}
	o.Message = fmt.Sprintf("rolling out: %d of %d replicas updated, %d available", s.UpdatedReplicas, want, s.AvailableReplicas)
	return o
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func failed(o Observation, r v1alpha1.Reason, msg string) Observation {
	o.Phase, o.Reason, o.Message = v1alpha1.PhaseFailed, r, msg
	return o
}

func revisionLabel(p *corev1.Pod) int64 {
	var n int64
	_, err := fmt.Sscan(p.Labels[LabelRevision], &n)
	if err != nil {
		return 0
	}
	return n
}

func crashMessage(c corev1.ContainerStatus) string {
	t := c.LastTerminationState.Terminated
	if t == nil {
		t = c.State.Terminated
	}
	if t == nil {
		return fmt.Sprintf("the app restarted %d times", c.RestartCount)
	}
	msg := fmt.Sprintf("the app exited with code %d", t.ExitCode)
	if line := firstLine(t.Message, ""); line != "" {
		msg += ": " + line
	}
	return msg
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	// The runtime's gRPC wrapping says nothing to a person.
	if i := strings.Index(s, " desc = "); strings.HasPrefix(s, "rpc error: ") && i >= 0 {
		s = s[i+len(" desc = "):]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		return fallback
	}
	return s
}
