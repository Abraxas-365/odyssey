// Package releasefleet is the release.Cluster adapter on the fleet: the
// desired app goes to the cluster the environment is placed on, through
// its agent's tunnel, and its status is what that agent reported. It
// depends on fleet's Dispatch port only (never on fleetsvc).
package releasefleet

import (
	"context"
	"errors"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/release"
)

type Cluster struct{ fleet fleet.Dispatch }

func New(dispatch fleet.Dispatch) *Cluster { return &Cluster{fleet: dispatch} }

var _ release.Cluster = (*Cluster)(nil)

// Apply stores the desired app for its cluster. What retrying cannot fix
// (no cluster, a revoked one, an app the fleet refuses) fails the
// deployment with APPLY_FAILED and the fleet's own words; anything else
// (the database briefly unreachable) is retried by the workflow.
func (c *Cluster) Apply(ctx context.Context, t release.Target, app release.DesiredApp) error {
	err := c.fleet.Apply(ctx, fleet.App{
		Organization: t.Organization, Project: t.Project, Environment: t.Environment, Name: app.Name,
		Revision: app.Revision, Image: app.Image, Port: app.Port, HealthPath: app.HealthPath,
		Replicas: app.Replicas, Env: app.Env, Labels: app.Labels, Access: app.Access,
	})
	var e *errx.Error
	if errors.As(err, &e) && (e.Type == errx.TypeBusiness || e.Type == errx.TypeValidation) {
		if e.Code == fleet.CodeNoCluster || e.Code == fleet.CodeClusterRevoked || e.Code == fleet.CodeAccessUnavailable {
			return release.Failed(release.CodeApplyFailed, e.Message)
		}
		return release.Failed(release.CodeApplyFailed, "El clúster no pudo aceptar la configuración de la app.", e.Message)
	}
	return err
}

func (c *Cluster) CanProtect(ctx context.Context, t release.Target) (bool, error) {
	return c.fleet.CanProtect(ctx, t.Organization, t.Environment)
}

func (c *Cluster) Status(ctx context.Context, t release.Target, name string) (release.AppStatus, error) {
	s, err := c.fleet.Status(ctx, t.Organization, t.Environment, name)
	if err != nil {
		return release.AppStatus{}, err
	}
	out := release.AppStatus{ObservedRevision: s.ObservedRevision, Message: s.Message, ReadyReplicas: s.ReadyReplicas,
		URL: s.URL, Phase: release.PhaseProgressing}
	switch s.Phase {
	case fleet.PhaseReady:
		out.Phase = release.PhaseReady
	case fleet.PhaseFailed:
		out.Phase = release.PhaseFailed
		out.Reason = map[fleet.Reason]release.FailureReason{
			fleet.ReasonImagePull: release.FailureImagePull,
			fleet.ReasonCrash:     release.FailureCrash,
			fleet.ReasonConfig:    release.FailureConfig,
		}[s.Reason]
		if out.Reason == "" {
			out.Reason = release.FailureUnhealthy
		}
	}
	return out, nil
}
