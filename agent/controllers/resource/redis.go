package resource

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// A redis resource <name> is a password-protected Redis with its data on a
// volume: the Secret <name>-redis (password, url), the PersistentVolumeClaim,
// the Deployment (one replica, Recreate: a volume is ReadWriteOnce) and the
// Service, all named <name>-redis. The password is made once and never
// changes; the Secret is read from the API (the agent never caches Secrets).

const (
	redisPort = 6379
	// redisUID runs Redis: the image's own user.
	redisUID = int64(999)
	// redisMaxMemory keeps Redis below the container's memory limit.
	redisMaxMemory = "400mb"
)

// redisURL is how an app in the namespace reaches the resource.
func redisURL(o *v1alpha1.OdysseyResource, password string) string {
	return fmt.Sprintf("redis://:%s@%s.%s.svc:%d", password, RedisName(o.Name), o.Namespace, redisPort)
}

func newPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil // URL-safe, and safe in a command line
}

// redisSecret keeps the Secret: the password is generated the first time and
// kept; the url is rewritten if it is missing or does not match.
func (r *Reconciler) redisSecret(ctx context.Context, o *v1alpha1.OdysseyResource) error {
	key := client.ObjectKey{Namespace: o.Namespace, Name: RedisName(o.Name)}
	var cur corev1.Secret
	err := r.API.Get(ctx, key, &cur)
	switch {
	case apierrors.IsNotFound(err):
		pw, err := newPassword()
		if err != nil {
			return err
		}
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labels(o)},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{RedisKeyPassword: []byte(pw), RedisKeyURL: []byte(redisURL(o, pw))}}
		if err := controllerutil.SetControllerReference(o, s, r.Client.Scheme()); err != nil {
			return err
		}
		if err := r.Client.Create(ctx, s); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return errCacheBehind
			}
			return err
		}
		return nil
	case err != nil:
		return err
	case !metav1.IsControlledBy(&cur, o):
		return &notOurs{"Secret", key.Name}
	}
	pw := string(cur.Data[RedisKeyPassword])
	if pw == "" {
		// Never replace a password a running Redis may hold; one that was
		// lost cannot be recovered, so a new one is made (the Deployment
		// reads it from here, and the app's pods roll with it).
		var err error
		if pw, err = newPassword(); err != nil {
			return err
		}
	}
	if string(cur.Data[RedisKeyPassword]) == pw && string(cur.Data[RedisKeyURL]) == redisURL(o, pw) {
		return nil
	}
	if cur.Data == nil {
		cur.Data = map[string][]byte{}
	}
	cur.Data[RedisKeyPassword], cur.Data[RedisKeyURL] = []byte(pw), []byte(redisURL(o, pw))
	return r.Client.Update(ctx, &cur)
}

func (r *Reconciler) redis(ctx context.Context, o *v1alpha1.OdysseyResource) (observation, error) {
	if err := r.redisSecret(ctx, o); err != nil {
		return observation{}, err
	}
	pvc, fail, err := r.claim(ctx, o, RedisName(o.Name))
	if err != nil {
		return observation{}, err
	}
	if fail != nil {
		return *fail, nil
	}
	_ = pvc
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: RedisName(o.Name), Namespace: o.Namespace}}
	if err := r.ensure(ctx, o, d, "Deployment", func() { mutateRedis(d, o) }); err != nil {
		return observation{}, err
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: RedisName(o.Name), Namespace: o.Namespace}}
	if err := r.ensure(ctx, o, svc, "Service", func() {
		svc.Labels = labels(o)
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Selector = map[string]string{LabelResource: o.Name}
		svc.Spec.Ports = []corev1.ServicePort{{Name: "redis", Port: redisPort, TargetPort: intstr.FromString("redis"), Protocol: corev1.ProtocolTCP}}
	}); err != nil {
		return observation{}, err
	}
	return redisState(d), nil
}

// redisState: ready when the Deployment saw its template and has a replica
// available.
func redisState(d *appsv1.Deployment) observation {
	s := d.Status
	for _, c := range s.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
			return failed("the cache did not start in time")
		}
	}
	if s.ObservedGeneration >= d.Generation && s.AvailableReplicas >= 1 {
		return ready()
	}
	return provisioning("starting the cache")
}

func mutateRedis(d *appsv1.Deployment, o *v1alpha1.OdysseyResource) {
	no, yes := false, true
	uid := redisUID
	l := labels(o)
	d.Labels = l
	d.Spec.Replicas = ptr(int32(1))
	// Only the resource label: a Deployment's selector is immutable.
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{LabelResource: o.Name}}
	d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	d.Spec.RevisionHistoryLimit = ptr(int32(1))
	d.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: l},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no,
			EnableServiceLinks:           &no,
			// Compatible with the baseline and restricted Pod Security profiles.
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &yes,
				RunAsUser:      &uid,
				RunAsGroup:     &uid,
				FSGroup:        &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "redis",
				Image:           RedisImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args: []string{
					"--requirepass", "$(REDIS_PASSWORD)",
					"--appendonly", "yes",
					"--dir", "/data",
					"--maxmemory", redisMaxMemory,
				},
				Ports: []corev1.ContainerPort{{Name: "redis", ContainerPort: redisPort, Protocol: corev1.ProtocolTCP}},
				Env: []corev1.EnvVar{{Name: "REDIS_PASSWORD", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: RedisName(o.Name)}, Key: RedisKeyPassword},
				}}},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: redisPing}},
					InitialDelaySeconds: 1, PeriodSeconds: 3, TimeoutSeconds: 3, SuccessThreshold: 1, FailureThreshold: 3,
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: redisPing}},
					InitialDelaySeconds: 10, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 6,
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					ReadOnlyRootFilesystem:   &yes,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: RedisName(o.Name)},
			}}},
		},
	}
}

// redisPing asks Redis (authenticated through the environment, not the
// command line) and succeeds only on PONG.
var redisPing = []string{"sh", "-c", `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping | grep -q PONG`}

func ptr[T any](v T) *T { return &v }
