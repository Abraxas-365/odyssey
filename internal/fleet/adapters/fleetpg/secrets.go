package fleetpg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

const deliveryColumns = `id, organization_id, project_id, environment_id, cluster_id, app, name, version, key_id, sealed, created_by, created_at`

// PutSecretDelivery inserts the delivery or replaces an older version of the
// same secret (the older one is never sent again), then signals.
func (r *Repository) PutSecretDelivery(ctx context.Context, d fleet.SecretDelivery) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.secret_deliveries (id, organization_id, project_id, environment_id, cluster_id, app, name, version, key_id, sealed, created_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (environment_id, app, name) DO UPDATE SET id = EXCLUDED.id, project_id = EXCLUDED.project_id,
		     cluster_id = EXCLUDED.cluster_id, version = EXCLUDED.version, key_id = EXCLUDED.key_id, sealed = EXCLUDED.sealed,
		     created_by = EXCLUDED.created_by, created_at = EXCLUDED.created_at
		 WHERE fleet.secret_deliveries.organization_id = EXCLUDED.organization_id
		   AND fleet.secret_deliveries.version < EXCLUDED.version`,
		d.ID, d.Organization, d.Project, d.Environment, d.Cluster, d.App, d.Name, d.Version, d.KeyID, d.Sealed, d.CreatedBy, d.CreatedAt)
	if err != nil {
		return false, pgerr.Map(err, "put secret delivery")
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, pgerr.Map(err, "put secret delivery")
	}
	// a newer value is on its way: an older failure no longer describes it
	if err := r.ClearSecretFailure(ctx, d.Organization, d.Environment, d.App, d.Name, d.Version); err != nil {
		return false, err
	}
	return true, r.notify(ctx, fleet.Signal{Organization: d.Organization, Cluster: d.Cluster, Secret: d.ID})
}

const failureColumns = `organization_id, environment_id, cluster_id, app, name, version, error, failed_at`

func (r *Repository) RecordSecretFailure(ctx context.Context, f fleet.SecretFailure) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.secret_failures (organization_id, environment_id, cluster_id, app, name, version, error)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (environment_id, app, name) DO UPDATE SET cluster_id = EXCLUDED.cluster_id, version = EXCLUDED.version,
		     error = EXCLUDED.error, failed_at = now()
		 WHERE fleet.secret_failures.organization_id = EXCLUDED.organization_id
		   AND fleet.secret_failures.version <= EXCLUDED.version`,
		f.Organization, f.Environment, f.Cluster, f.App, f.Name, f.Version, f.Error)
	return pgerr.Map(err, "record secret failure")
}

func (r *Repository) ClearSecretFailure(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID, app, name string, version int64) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `DELETE FROM fleet.secret_failures
		WHERE organization_id = $1 AND environment_id = $2 AND app = $3 AND name = $4 AND version <= $5`,
		organization, env, app, name, version)
	return pgerr.Map(err, "clear secret failure")
}

func (r *Repository) ListSecretFailures(ctx context.Context, organization identity.OrganizationID, env project.EnvironmentID) ([]fleet.SecretFailure, error) {
	out := []fleet.SecretFailure{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+failureColumns+` FROM fleet.secret_failures
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY name`, organization, env)
	return out, pgerr.Map(err, "list secret failures")
}

func (r *Repository) ListSecretDeliveries(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, since time.Time) ([]fleet.SecretDelivery, error) {
	out := []fleet.SecretDelivery{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `SELECT `+deliveryColumns+` FROM fleet.secret_deliveries
		WHERE organization_id = $1 AND cluster_id = $2 AND created_at > $3 ORDER BY created_at, id`, organization, cluster, since)
	return out, pgerr.Map(err, "list secret deliveries")
}

func (r *Repository) FindSecretDelivery(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, id fleet.SecretDeliveryID) (fleet.SecretDelivery, error) {
	var out fleet.SecretDelivery
	err := txn.Q(ctx, r.db).GetContext(ctx, &out, `SELECT `+deliveryColumns+` FROM fleet.secret_deliveries
		WHERE organization_id = $1 AND cluster_id = $2 AND id = $3`, organization, cluster, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.SecretDelivery{}, errx.NotFound("secret delivery not found")
	}
	return out, pgerr.Map(err, "find secret delivery")
}

// DeleteSecretDelivery only acts for the cluster's current tunnel: an ack
// from a superseded tunnel is ignored (its agent is gone; the new one gets
// the value again).
func (r *Repository) DeleteSecretDelivery(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, session fleet.Session, id fleet.SecretDeliveryID) (fleet.SecretDelivery, bool, error) {
	var out fleet.SecretDelivery
	err := txn.Q(ctx, r.db).GetContext(ctx, &out, `DELETE FROM fleet.secret_deliveries d
		WHERE d.organization_id = $1 AND d.cluster_id = $2 AND d.id = $4
		  AND EXISTS (SELECT 1 FROM fleet.clusters c WHERE c.organization_id = $1 AND c.id = $2 AND c.status = 'active' AND c.session_id = $3)
		RETURNING `+deliveryColumns, organization, cluster, session, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.SecretDelivery{}, false, nil
	}
	if err != nil {
		return fleet.SecretDelivery{}, false, pgerr.Map(err, "delete secret delivery")
	}
	return out, true, nil
}

func (r *Repository) DeleteSecretDeliveries(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, cutoff time.Time) ([]fleet.SecretDelivery, error) {
	out := []fleet.SecretDelivery{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `DELETE FROM fleet.secret_deliveries
		WHERE organization_id = $1 AND cluster_id = $2 AND created_at < $3 RETURNING `+deliveryColumns, organization, cluster, cutoff)
	return out, pgerr.Map(err, "delete expired secret deliveries")
}

func (r *Repository) DropSecretDeliveries(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID) ([]fleet.SecretDelivery, error) {
	out := []fleet.SecretDelivery{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `DELETE FROM fleet.secret_deliveries
		WHERE organization_id = $1 AND cluster_id = $2 RETURNING `+deliveryColumns, organization, cluster)
	return out, pgerr.Map(err, "drop secret deliveries")
}

// ExpireSecretDeliveries is the sweep's cross-organization delete (the
// scheduled task has no tenant: it acts on every one, returning each row's).
func (r *Repository) ExpireSecretDeliveries(ctx context.Context, cutoff time.Time, limit int) ([]fleet.SecretDelivery, error) {
	out := []fleet.SecretDelivery{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &out, `DELETE FROM fleet.secret_deliveries WHERE id IN (
		SELECT id FROM fleet.secret_deliveries WHERE created_at < $1 ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+deliveryColumns, cutoff, limit)
	return out, pgerr.Map(err, "expire secret deliveries")
}
