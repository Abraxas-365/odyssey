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
	secrets := make([]fleet.AppSecret, 0, len(app.Secrets))
	for _, s := range app.Secrets {
		secrets = append(secrets, fleet.AppSecret{Name: s.Name, Source: string(s.Source), Version: s.Version, KeyID: s.KeyID,
			Store: s.Store, Key: s.Key, Property: s.Property})
	}
	resources := make([]fleet.ResourceRef, 0, len(app.Resources))
	for _, r := range app.Resources {
		resources = append(resources, fleet.ResourceRef{Name: r.Name, Kind: r.Kind, Prefix: r.Prefix})
	}
	err := c.fleet.Apply(ctx, fleet.App{
		Organization: t.Organization, Project: t.Project, Environment: t.Environment, Name: app.Name,
		Revision: app.Revision, Image: app.Image, Port: app.Port, HealthPath: app.HealthPath,
		Replicas: app.Replicas, Env: app.Env, Labels: app.Labels, Access: app.Access, Secrets: secrets, Resources: resources,
	})
	var e *errx.Error
	if errors.As(err, &e) && (e.Type == errx.TypeBusiness || e.Type == errx.TypeValidation) {
		switch e.Code {
		case fleet.CodeNoCluster, fleet.CodeClusterRevoked, fleet.CodeAccessUnavailable, fleet.CodeSecretsUnavailable,
			fleet.CodeExternalSecretsUnavailable, fleet.CodeSecretKeyChanged, fleet.CodeSecretStoreUnknown,
			fleet.CodeResourcesUnavailable:
			return release.Failed(release.CodeApplyFailed, e.Message)
		}
		return release.Failed(release.CodeApplyFailed, "El clúster no pudo aceptar la configuración de la app.", e.Message)
	}
	return err
}

// Secrets is what the environment's cluster offers (fleet.SecretTarget).
func (c *Cluster) Secrets(ctx context.Context, t release.Target) (release.ClusterSecrets, error) {
	st, err := c.fleet.SecretTarget(ctx, t.Organization, t.Environment)
	if err != nil {
		return release.ClusterSecrets{}, err
	}
	out := release.ClusterSecrets{External: st.External, Stores: st.Stores, Revoked: st.Revoked}
	if st.Key != nil && !st.Cluster.IsZero() {
		out.Key = &release.ClusterKey{Cluster: st.Cluster.String(), KeyID: st.Key.KeyID, Public: st.Key.Public}
	}
	if out.Stores == nil {
		out.Stores = []string{}
	}
	if len(st.Failures) > 0 {
		out.Failures = make(map[string]release.SecretFailure, len(st.Failures))
		for _, f := range st.Failures {
			out.Failures[f.Name] = release.SecretFailure{App: f.App, Version: f.Version, Error: f.Error, FailedAt: f.FailedAt}
		}
	}
	return out, nil
}

// DeliverSecret hands the sealed value to the fleet. Its refusals keep their
// code (the console asks the person to reload the page, or to call IT).
func (c *Cluster) DeliverSecret(ctx context.Context, t release.Target, d release.SecretDelivery) error {
	err := c.fleet.DeliverSecret(ctx, fleet.SecretDelivery{
		Organization: t.Organization, Project: t.Project, Environment: t.Environment, App: d.App,
		Name: d.Name, Version: d.Version, KeyID: d.KeyID, Sealed: d.Sealed, CreatedBy: d.By,
	})
	var e *errx.Error
	if errors.As(err, &e) && e.Type == errx.TypeBusiness {
		return errx.Business(e.Message).WithCode(e.Code) // same codes on both sides (SECRET_KEY_CHANGED, ...)
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
			fleet.ReasonResource:  release.FailureResource,
		}[s.Reason]
		if out.Reason == "" {
			out.Reason = release.FailureUnhealthy
		}
	}
	return out, nil
}
