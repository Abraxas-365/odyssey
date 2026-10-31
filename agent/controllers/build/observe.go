package build

import (
	"fmt"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	"github.com/Abraxas-365/odyssey/agent/builder"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// maxMessage bounds a reported message (the gateway keeps 2000 runes).
const maxMessage = 1500

// Observe is the status of a build whose Job is job (nil: not created yet)
// and whose pod is pod (nil: none yet). It is a pure function of what the
// cluster shows.
//
//   - a container's termination message (builder.Result) is the most precise
//     word: prepare's failure (source, unsupported), build's digest or
//     failure (push, failed);
//   - otherwise Kubernetes' own: the Job's deadline (timeout), an eviction,
//     the OOM killer, an image that cannot be pulled.
func Observe(job *batchv1.Job, pod *corev1.Pod) v1alpha1.OdysseyBuildStatus {
	if job == nil {
		return v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildPending}
	}
	if c := jobCondition(job, batchv1.JobFailed); c != nil && c.Reason == batchv1.JobReasonDeadlineExceeded {
		msg := "the build ran past its time limit"
		if why := stuck(pod); why != "" {
			msg += " (" + why + ")"
		}
		return failed(v1alpha1.BuildReasonTimeout, msg)
	}
	if pod != nil {
		if s, ok := fromPod(pod); ok {
			return s
		}
	}
	if c := jobCondition(job, batchv1.JobFailed); c != nil {
		return failed(v1alpha1.BuildReasonFailed, strings.TrimSpace(c.Reason+": "+c.Message))
	}
	if jobCondition(job, batchv1.JobComplete) != nil {
		// complete without a pod to read the digest from (gone already)
		return failed(v1alpha1.BuildReasonFailed, "the build finished but its result was lost")
	}
	if pod != nil && (pod.Status.Phase == corev1.PodRunning || startedAny(pod)) {
		return v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildRunning}
	}
	return v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildPending}
}

func failed(reason v1alpha1.BuildReason, msg string) v1alpha1.OdysseyBuildStatus {
	return v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildFailed, Reason: reason, Message: builder.Tail(msg, maxMessage)}
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if c := &job.Status.Conditions[i]; c.Type == t && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

func startedAny(pod *corev1.Pod) bool {
	for _, s := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		if s.State.Running != nil || s.State.Terminated != nil {
			return true
		}
	}
	return false
}

// stuck says why a container of the pod never started ("" when none is
// waiting on something Kubernetes reports: an image it cannot pull, a
// configuration it cannot create the container with).
func stuck(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	for _, s := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		w := s.State.Waiting
		if w == nil {
			continue
		}
		switch w.Reason {
		case "ErrImagePull", "ImagePullBackOff":
			return fmt.Sprintf("the cluster could not pull the image %s: %s", s.Image, w.Message)
		case "CreateContainerConfigError", "CreateContainerError":
			return "the cluster could not start the container " + s.Name + ": " + w.Message
		}
	}
	return ""
}

func status(statuses []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}
	return nil
}

// fromPod reads a final status from the pod, when it has one.
func fromPod(pod *corev1.Pod) (v1alpha1.OdysseyBuildStatus, bool) {
	if pod.Status.Reason == "Evicted" {
		return failed(v1alpha1.BuildReasonFailed, "the cluster evicted the build: "+pod.Status.Message), true
	}
	if s := status(pod.Status.InitContainerStatuses, ContainerPrepare); s != nil {
		if out, ok := fromContainer(s, "preparing the source", v1alpha1.BuildReasonSource); ok {
			return out, true
		}
	}
	if s := status(pod.Status.ContainerStatuses, ContainerBuild); s != nil {
		if out, ok := fromContainer(s, "building", v1alpha1.BuildReasonFailed); ok {
			return out, true
		}
	}
	return v1alpha1.OdysseyBuildStatus{}, false
}

// fromContainer is the final status a container shows: failed (by its own
// words or Kubernetes'), or succeeded when it pushed (build only). An image
// that cannot be pulled (yet) is not final: the kubelet keeps trying (a
// registry's 5xx passes) and the Job's deadline ends it, saying why
// (Observe).
func fromContainer(s *corev1.ContainerStatus, step string, fallback v1alpha1.BuildReason) (v1alpha1.OdysseyBuildStatus, bool) {
	if w := s.State.Waiting; w != nil {
		if w.Reason == "InvalidImageName" {
			return failed(v1alpha1.BuildReasonFailed, fmt.Sprintf("the builder image %s is not a valid image name: %s", s.Image, w.Message)), true
		}
		return v1alpha1.OdysseyBuildStatus{}, false
	}
	t := s.State.Terminated
	if t == nil {
		return v1alpha1.OdysseyBuildStatus{}, false
	}
	r, ok := builder.ParseResult(t.Message)
	if t.ExitCode == 0 {
		if s.Name != ContainerBuild {
			return v1alpha1.OdysseyBuildStatus{}, false // prepare done: build follows
		}
		if ok && r.Digest != "" && digestPattern(r.Digest) {
			return v1alpha1.OdysseyBuildStatus{Phase: v1alpha1.BuildSucceeded, ImageDigest: r.Digest}, true
		}
		return failed(v1alpha1.BuildReasonFailed, "the builder ended without reporting the image it pushed"), true
	}
	if ok && r.Reason != "" {
		return failed(reasonOf(r.Reason, fallback), r.Message), true
	}
	switch {
	case t.Reason == "OOMKilled":
		return failed(v1alpha1.BuildReasonFailed, step+" ran out of memory"), true
	case ok && r.Message != "":
		return failed(fallback, r.Message), true
	}
	return failed(fallback, fmt.Sprintf("%s failed (exit code %d, %s)", step, t.ExitCode, t.Reason)), true
}

func reasonOf(r string, fallback v1alpha1.BuildReason) v1alpha1.BuildReason {
	switch v1alpha1.BuildReason(r) {
	case v1alpha1.BuildReasonUnsupported, v1alpha1.BuildReasonSource, v1alpha1.BuildReasonPush, v1alpha1.BuildReasonFailed:
		return v1alpha1.BuildReason(r)
	}
	return fallback
}

func digestPattern(d string) bool {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
