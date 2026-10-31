// Package builds is the agent's side of the fleet protocol's builds: it
// turns a Build the control plane sends into an OdysseyBuild (which
// agent/controllers/build runs), deletes it when cancelled, reads its
// status back, and follows its pod's log for the tunnel.
package builds

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Manager implements the tunnel's Builds.
type Manager struct {
	Client client.Client
	// Registry is where this cluster's images go: a build must push under it.
	Registry string
	// Logs opens a container's log (agent wiring: the pods/log API).
	Logs LogOpener
}

var (
	idPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	shaPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	repoPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*$`)
)

// Check refuses a build the agent will not run (the control plane never
// sends one; a refused build is reported failed). Its image goes exactly
// where the control plane names a project's images
// (<registry>/<organization>/<project>), so a build can never write over
// another project's.
func (m *Manager) Check(b *fleetv1.Build) error {
	src := b.GetSource()
	repo := strings.TrimSuffix(m.Registry, "/") + "/" + b.GetOrganizationId() + "/" + b.GetProjectId()
	switch {
	case !idPattern.MatchString(b.GetBuildId()) || !idPattern.MatchString(b.GetOrganizationId()) ||
		!idPattern.MatchString(b.GetProjectId()) || !idPattern.MatchString(b.GetEnvironmentId()):
		return fmt.Errorf("the build's ids are not valid")
	case !shaPattern.MatchString(src.GetSha256()) || src.GetSize() <= 0 || src.GetSize() > srcarchive.Platform().Compressed:
		return fmt.Errorf("the build's source reference is not valid")
	case m.Registry == "" || b.GetImageRepository() != repo || !repoPattern.MatchString(repo) || len(repo) > 200:
		return fmt.Errorf("the build's image repository %q is not its project's in this cluster's registry (%q)", b.GetImageRepository(), repo)
	case b.GetCacheRef() != "" && b.GetCacheRef() != repo+":buildcache":
		return fmt.Errorf("the build's cache reference is not its project's")
	case b.GetTimeoutSeconds() < 60 || b.GetTimeoutSeconds() > 7200:
		return fmt.Errorf("the build's timeout is out of range")
	}
	return nil
}

// Start creates the build's OdysseyBuild (once: an existing one is the same
// build). A build the agent refuses comes back as its failed status.
func (m *Manager) Start(ctx context.Context, b *fleetv1.Build) (*fleetv1.BuildStatus, error) {
	if err := m.Check(b); err != nil {
		return refused(b.GetBuildId(), err.Error()), nil
	}
	ob := &v1alpha1.OdysseyBuild{
		ObjectMeta: metav1.ObjectMeta{Name: b.GetBuildId(), Namespace: buildctl.Namespace,
			Labels: map[string]string{buildctl.LabelManagedBy: buildctl.ManagedBy, "odyssey.dev/project": b.GetProjectId()}},
		Spec: v1alpha1.OdysseyBuildSpec{
			Organization: b.GetOrganizationId(), Project: b.GetProjectId(), Environment: b.GetEnvironmentId(),
			Source:          v1alpha1.BuildSource{SHA256: b.GetSource().GetSha256(), Size: b.GetSource().GetSize()},
			ImageRepository: b.GetImageRepository(), CacheRef: b.GetCacheRef(), TimeoutSeconds: b.GetTimeoutSeconds(),
		},
	}
	switch err := m.Client.Create(ctx, ob); {
	case err == nil, apierrors.IsAlreadyExists(err):
		return nil, nil
	case apierrors.IsInvalid(err):
		return refused(b.GetBuildId(), "the cluster refused the build: "+err.Error()), nil
	default:
		return nil, err
	}
}

func refused(id, msg string) *fleetv1.BuildStatus {
	return &fleetv1.BuildStatus{BuildId: id, Phase: string(v1alpha1.BuildFailed), Reason: string(v1alpha1.BuildReasonFailed), Message: msg}
}

// Cancel deletes the build: its Job, pod and token go with it.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) {
		return nil
	}
	ob := &v1alpha1.OdysseyBuild{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: buildctl.Namespace}}
	return client.IgnoreNotFound(m.Client.Delete(ctx, ob, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// Status is the build's (nil when the cluster does not have it).
func (m *Manager) Status(ctx context.Context, id string) (*fleetv1.BuildStatus, error) {
	if !idPattern.MatchString(id) {
		return nil, nil
	}
	var ob v1alpha1.OdysseyBuild
	if err := m.Client.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: id}, &ob); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return ToProto(&ob), nil
}

// Statuses are every build's.
func (m *Manager) Statuses(ctx context.Context) ([]*fleetv1.BuildStatus, error) {
	var list v1alpha1.OdysseyBuildList
	if err := m.Client.List(ctx, &list, client.InNamespace(buildctl.Namespace)); err != nil {
		return nil, err
	}
	out := make([]*fleetv1.BuildStatus, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp.IsZero() {
			out = append(out, ToProto(&list.Items[i]))
		}
	}
	return out, nil
}

// ToProto is a build's status as the protocol reports it.
func ToProto(b *v1alpha1.OdysseyBuild) *fleetv1.BuildStatus {
	phase := b.Status.Phase
	if phase == "" {
		phase = v1alpha1.BuildPending
	}
	return &fleetv1.BuildStatus{BuildId: b.Name, Phase: string(phase), ImageDigest: b.Status.ImageDigest,
		Reason: string(b.Status.Reason), Message: b.Status.Message}
}
