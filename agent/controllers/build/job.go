package build

import (
	"strconv"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	// Namespace holds every build (OdysseyBuilds, their Jobs and token
	// Secrets). It needs Pod Security "privileged": BuildKit rootless runs
	// with seccomp and AppArmor unconfined (agent/install/builds.yaml).
	Namespace = "odyssey-builds"

	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedBy      = "odyssey-agent"
	// LabelBuild names the build a Job, Pod or Secret belongs to.
	LabelBuild = "odyssey.dev/build"

	// Containers of a build's pod.
	ContainerReuse   = "reuse"
	ContainerPrepare = "prepare"
	ContainerBuild   = "build"

	// TokenKey is the key of the token in a build's Secret.
	TokenKey = "token"
	// RegistrySecret holds the registry's credentials (a
	// kubernetes.io/dockerconfigjson Secret in Namespace; optional).
	RegistrySecret = "odyssey-registry"

	// uid of BuildKit's rootless image (its user "user"); prepare runs as it
	// too so the workspace it writes is the builder's.
	builderUID = 1000

	workDir = "/work"
)

// Images and settings of build Jobs.
type Config struct {
	// AgentImage runs prepare (it is the agent's own image).
	AgentImage string
	// BuildKitImage runs build (moby/buildkit rootless, pinned).
	BuildKitImage string
	// Frontend is the Railpack BuildKit frontend image (pinned; the same
	// version as the library that writes the plan).
	Frontend string
	// SourceURL is where prepare fetches the source (the agent's source
	// server, reached through its Service).
	SourceURL string
	// InsecureRegistry pushes over plain HTTP (a local registry).
	InsecureRegistry bool
	// Platform built for (e.g. linux/amd64).
	Platform string
	// TTL keeps a finished Job (and its pod's logs) this long.
	TTL time.Duration
	// Retention keeps a finished OdysseyBuild this long (its status is
	// reported on every reconnect meanwhile); longer than TTL.
	Retention time.Duration
	// Scan runs Trivy over every image built (scan.go); zero: no scans.
	Scan ScanConfig
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = time.Hour
	}
	if c.Retention <= c.TTL {
		c.Retention = 3 * c.TTL
	}
	if c.Scan.Deadline <= 0 {
		c.Scan.Deadline = 15 * time.Minute
	}
	if c.Scan.TTL <= 0 {
		c.Scan.TTL = c.Retention + time.Hour
	}
	return c
}

// JobName is the Job (and Secret) of a build.
func JobName(b *v1alpha1.OdysseyBuild) string { return "build-" + b.Name }

func labels(b *v1alpha1.OdysseyBuild) map[string]string {
	return map[string]string{LabelManagedBy: ManagedBy, LabelBuild: b.Name}
}

// TokenSecret is the build's one-time credential to fetch its source.
func TokenSecret(b *v1alpha1.OdysseyBuild, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: JobName(b), Namespace: Namespace, Labels: labels(b)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{TokenKey: []byte(token)},
	}
}

// Job runs one build: prepare (fetch, verify, plan) then build (BuildKit,
// push). Nothing in the pod talks to the Kubernetes API, nothing keeps
// state outside its emptyDirs, and it runs once (no retries: the control
// plane decides what to do with a failure).
func Job(b *v1alpha1.OdysseyBuild, c Config) *batchv1.Job {
	image := b.Spec.ImageRepository + ":" + b.Name
	work := corev1.VolumeMount{Name: "work", MountPath: workDir}
	prepareEnv := []corev1.EnvVar{
		{Name: "ODYSSEY_SOURCE_URL", Value: c.SourceURL + "/builds/" + b.Name + "/source"},
		{Name: "ODYSSEY_SOURCE_SHA256", Value: b.Spec.Source.SHA256},
		{Name: "ODYSSEY_SOURCE_SIZE", Value: strconv.FormatInt(b.Spec.Source.Size, 10)},
		{Name: "ODYSSEY_SOURCE_TOKEN_FILE", Value: "/var/run/odyssey/token/" + TokenKey},
		{Name: "ODYSSEY_WORK", Value: workDir},
		{Name: "HOME", Value: "/tmp"},
	}
	buildEnv := []corev1.EnvVar{
		{Name: "ODYSSEY_WORK", Value: workDir},
		{Name: "ODYSSEY_IMAGE", Value: image},
		{Name: "ODYSSEY_CACHE_REF", Value: b.Spec.CacheRef},
		{Name: "ODYSSEY_CACHE_KEY", Value: b.Spec.Project},
		{Name: "ODYSSEY_FRONTEND", Value: c.Frontend},
		{Name: "ODYSSEY_PLATFORM", Value: c.Platform},
		{Name: "ODYSSEY_REGISTRY_INSECURE", Value: strconv.FormatBool(c.InsecureRegistry)},
		// BuildKit rootless inside a container without a user namespace of
		// its own: the process sandbox needs privileges the pod lacks.
		{Name: "BUILDKITD_FLAGS", Value: "--oci-worker-no-process-sandbox"},
		{Name: "DOCKER_CONFIG", Value: "/var/run/odyssey/registry"},
	}
	ttl := int32(c.TTL.Seconds())
	// restricted: the same as prepare's
	restricted := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	initContainers := []corev1.Container{{
		Name:    ContainerPrepare,
		Image:   c.AgentImage,
		Command: []string{"odyssey-agent", "builder", "prepare"},
		Env:     prepareEnv,
		VolumeMounts: []corev1.VolumeMount{work,
			{Name: "tmp", MountPath: "/tmp"},
			{Name: "token", MountPath: "/var/run/odyssey/token", ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("2Gi")},
		},
		SecurityContext:          restricted.DeepCopy(),
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}}
	if b.Spec.ReuseImage != "" {
		// first: looks for the earlier image and, when it is there, tags it
		// as this build's; prepare and build then do nothing (builder/reuse.go)
		initContainers = append([]corev1.Container{{
			Name:    ContainerReuse,
			Image:   c.AgentImage,
			Command: []string{"odyssey-agent", "builder", "reuse"},
			Env: []corev1.EnvVar{
				{Name: "ODYSSEY_REUSE_IMAGE", Value: b.Spec.ReuseImage},
				{Name: "ODYSSEY_IMAGE", Value: image},
				{Name: "ODYSSEY_REGISTRY_INSECURE", Value: strconv.FormatBool(c.InsecureRegistry)},
				{Name: "ODYSSEY_WORK", Value: workDir},
				{Name: "DOCKER_CONFIG", Value: "/var/run/odyssey/registry"},
				{Name: "HOME", Value: "/tmp"},
			},
			VolumeMounts: []corev1.VolumeMount{work,
				{Name: "tmp", MountPath: "/tmp"},
				{Name: "registry", MountPath: "/var/run/odyssey/registry", ReadOnly: true},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("100Mi")},
			},
			SecurityContext:          restricted.DeepCopy(),
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		}}, initContainers...)
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: JobName(b), Namespace: Namespace, Labels: labels(b)},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   ptr.To(int64(b.Spec.TimeoutSeconds)),
			TTLSecondsAfterFinished: &ttl,
			PodReplacementPolicy:    ptr.To(batchv1.Failed),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(b)},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						RunAsUser:    ptr.To[int64](builderUID),
						RunAsGroup:   ptr.To[int64](builderUID),
						FSGroup:      ptr.To[int64](builderUID),
					},
					InitContainers: initContainers,
					Containers: []corev1.Container{{
						Name:    ContainerBuild,
						Image:   c.BuildKitImage,
						Command: []string{workDir + "/bin/odyssey-agent", "builder", "build"},
						Env:     buildEnv,
						VolumeMounts: []corev1.VolumeMount{work,
							{Name: "buildkit", MountPath: "/home/user/.local/share/buildkit"},
							{Name: "registry", MountPath: "/var/run/odyssey/registry", ReadOnly: true},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
							Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("10Gi")},
						},
						// BuildKit rootless creates user namespaces and mounts:
						// the runtime's default seccomp and AppArmor profiles
						// forbid that. Of the capabilities only SETUID/SETGID
						// stay (newuidmap/newgidmap are setcap binaries, which is
						// also why privilege escalation cannot be turned off).
						// Checked: a RUN step cannot read buildkitd's files
						// (/proc/<pid>/root: permission denied), so the registry
						// credential stays out of the build's reach. The root
						// filesystem stays writable: rootlesskit keeps its state
						// under $HOME.
						SecurityContext: &corev1.SecurityContext{
							SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
							AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
							Capabilities:    &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SETUID", "SETGID"}},
						},
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
					}},
					Volumes: []corev1.Volume{
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "buildkit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: JobName(b), DefaultMode: ptr.To[int32](0o440)}}},
						{Name: "registry", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: RegistrySecret, Optional: ptr.To(true), DefaultMode: ptr.To[int32](0o440),
							Items: []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}}}}},
					},
				},
			},
		},
	}
}
