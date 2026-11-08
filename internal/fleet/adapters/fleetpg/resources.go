package fleetpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/lib/pq"
)

const resourceColumns = `organization_id, project_id, environment_id, cluster_id, name, kind, generation, storage_gi, recipe, spec, deleted, updated_at`

// resourceRow reads spec, JSON in the table, into the desired resource.
type resourceRow struct {
	fleet.DesiredResource
	Spec []byte `db:"spec"`
}

func (x resourceRow) desired() (fleet.DesiredResource, error) {
	out := x.DesiredResource
	if err := json.Unmarshal(x.Spec, &out.Spec); err != nil {
		return fleet.DesiredResource{}, errx.Wrap(err, "decode resource spec", errx.TypeInternal)
	}
	return out, nil
}

// PutResource stores the desired resource unless the stored generation is
// the same or newer, or the row is another cluster's (the environment moved),
// and signals the cluster's gateway.
func (r *Repository) PutResource(ctx context.Context, d fleet.DesiredResource) (bool, error) {
	spec, err := json.Marshal(d.Spec)
	if err != nil {
		return false, errx.Wrap(err, "encode resource spec", errx.TypeInternal)
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.desired_resources (environment_id, name, organization_id, project_id, cluster_id, kind, generation, storage_gi, recipe, spec, deleted)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (environment_id, name) DO UPDATE SET generation = EXCLUDED.generation,
		        storage_gi = EXCLUDED.storage_gi, recipe = EXCLUDED.recipe, spec = EXCLUDED.spec, deleted = EXCLUDED.deleted, updated_at = now()
		 WHERE fleet.desired_resources.organization_id = EXCLUDED.organization_id
		   AND fleet.desired_resources.cluster_id = EXCLUDED.cluster_id
		   AND fleet.desired_resources.kind = EXCLUDED.kind
		   AND fleet.desired_resources.generation < EXCLUDED.generation`,
		d.Environment, d.Name, d.Organization, d.Project, d.Cluster, d.Kind, d.Generation, d.StorageGi, d.Recipe, spec, d.Deleted)
	if err != nil {
		return false, pgerr.Map(err, "put resource")
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, pgerr.Map(err, "put resource")
	}
	return true, r.notify(ctx, fleet.Signal{Organization: d.Organization, Cluster: d.Cluster, Environment: d.Environment, Name: d.Name, Resource: true})
}

func (r *Repository) ListResources(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID) ([]fleet.DesiredResource, error) {
	return r.listResources(ctx, `cluster_id = $2`, organization, cluster)
}

// ListEnvironmentResources are the environment's desired resources on
// cluster (what a move of the environment would send elsewhere).
func (r *Repository) ListEnvironmentResources(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, environment project.EnvironmentID) ([]fleet.DesiredResource, error) {
	return r.listResources(ctx, `cluster_id = $2 AND environment_id = $3`, organization, cluster, environment)
}

func (r *Repository) listResources(ctx context.Context, where string, args ...any) ([]fleet.DesiredResource, error) {
	var rows []resourceRow
	if err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+resourceColumns+` FROM fleet.desired_resources
		WHERE organization_id = $1 AND `+where+` ORDER BY environment_id, name`, args...); err != nil {
		return nil, pgerr.Map(err, "list resources")
	}
	out := make([]fleet.DesiredResource, 0, len(rows))
	for _, x := range rows {
		d, err := x.desired()
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *Repository) FindResource(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, environment project.EnvironmentID, name string) (fleet.DesiredResource, error) {
	var x resourceRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &x, `SELECT `+resourceColumns+` FROM fleet.desired_resources
		WHERE organization_id = $1 AND cluster_id = $2 AND environment_id = $3 AND name = $4`, organization, cluster, environment, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.DesiredResource{}, errx.NotFound("resource not found")
	}
	if err != nil {
		return fleet.DesiredResource{}, pgerr.Map(err, "find resource")
	}
	return x.desired()
}

func (r *Repository) FindResourceStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (fleet.ResourceStatus, error) {
	var s fleet.ResourceStatus
	err := txn.Q(ctx, r.db).GetContext(ctx, &s,
		`SELECT environment_id, name, observed_generation, phase, message, observed_at
		 FROM fleet.resource_status WHERE organization_id = $1 AND environment_id = $2 AND name = $3`, organization, environment, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.ResourceStatus{}, errx.NotFound("resource status not found")
	}
	return s, pgerr.Map(err, "find resource status")
}

// ObserveResources upserts the reported statuses of resources desired on
// this cluster, in one statement, while session is the cluster's tunnel. A
// report of a resource the cluster does not hold is dropped (an agent cannot
// write another cluster's, or another tenant's, status); so is one of a
// generation beyond the desired one. A status never goes back to an older
// generation. A full report lists every resource of the cluster: the statuses
// of resources desired here that it does not list are forgotten (the console
// then shows them as preparing again), in the same statement and only while
// session is the tunnel; a partial report never forgets. A resource being
// deleted that is reported "deleted" (at its generation or a later one) is
// removed, desired state and status, and
// returned.
func (r *Repository) ObserveResources(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, session fleet.Session, report fleet.ResourceReport) (bool, []fleet.DesiredResource, error) {
	n := len(report.Resources)
	envs, names, gens, phases, msgs := make([]string, n), make([]string, n), make([]int64, n), make([]string, n), make([]string, n)
	for i, s := range report.Resources {
		envs[i], names[i], gens[i], phases[i], msgs[i] = s.Environment.String(), s.Name, s.ObservedGeneration, string(s.Phase), s.Message
	}
	q := txn.Q(ctx, r.db)
	var live bool
	err := q.GetContext(ctx, &live,
		`WITH tunnel AS (
		    SELECT 1 FROM fleet.clusters WHERE organization_id = $1 AND id = $2 AND status = 'active' AND session_id = $3
		 ), reported AS (
		    SELECT DISTINCT ON (r.env, r.name) d.environment_id, d.name, d.organization_id, d.cluster_id, r.gen, r.phase, r.message
		    FROM unnest($4::uuid[], $5::text[], $6::bigint[], $7::text[], $8::text[]) AS r(env, name, gen, phase, message)
		    JOIN fleet.desired_resources d ON d.environment_id = r.env AND d.name = r.name
		    WHERE d.organization_id = $1 AND d.cluster_id = $2 AND r.gen <= d.generation AND r.phase <> 'deleted'
		    ORDER BY r.env, r.name, r.gen DESC
		 ), pruned AS (
		    DELETE FROM fleet.resource_status st USING fleet.desired_resources d
		    WHERE $9::boolean AND EXISTS (SELECT 1 FROM tunnel)
		      AND d.environment_id = st.environment_id AND d.name = st.name
		      AND d.organization_id = $1 AND d.cluster_id = $2
		      AND st.organization_id = $1 AND st.cluster_id = $2
		      AND NOT EXISTS (SELECT 1 FROM unnest($4::uuid[], $5::text[]) AS k(env, name) WHERE k.env = st.environment_id AND k.name = st.name)
		    RETURNING 1
		 ), upserted AS (
		    INSERT INTO fleet.resource_status (environment_id, name, organization_id, cluster_id, observed_generation, phase, message, observed_at)
		    SELECT environment_id, name, organization_id, cluster_id, gen, phase, message, now()
		    FROM reported
		    WHERE EXISTS (SELECT 1 FROM tunnel)
		    ON CONFLICT (environment_id, name) DO UPDATE SET
		        observed_generation = EXCLUDED.observed_generation, phase = EXCLUDED.phase, message = EXCLUDED.message, observed_at = now()
		    WHERE fleet.resource_status.organization_id = $1 AND fleet.resource_status.cluster_id = $2
		      AND fleet.resource_status.observed_generation <= EXCLUDED.observed_generation
		    RETURNING 1
		 )
		 SELECT EXISTS (SELECT 1 FROM tunnel)`,
		organization, id, session, pq.Array(envs), pq.Array(names), pq.Array(gens), pq.Array(phases), pq.Array(msgs), report.Full)
	if err != nil {
		return false, nil, pgerr.Map(err, "observe resources")
	}
	if !live {
		return false, nil, nil
	}
	var gone []resourceRow
	err = q.SelectContext(ctx, &gone,
		`DELETE FROM fleet.desired_resources d USING unnest($3::uuid[], $4::text[], $5::bigint[], $6::text[]) AS r(env, name, gen, phase)
		 WHERE d.organization_id = $1 AND d.cluster_id = $2 AND d.environment_id = r.env AND d.name = r.name
		   AND d.deleted AND r.phase = 'deleted' AND r.gen >= d.generation
		 RETURNING d.organization_id, d.project_id, d.environment_id, d.cluster_id, d.name, d.kind, d.generation, d.storage_gi, d.recipe, d.spec, d.deleted, d.updated_at`,
		organization, id, pq.Array(envs), pq.Array(names), pq.Array(gens), pq.Array(phases))
	if err != nil {
		return false, nil, pgerr.Map(err, "forget removed resources")
	}
	removed := make([]fleet.DesiredResource, 0, len(gone))
	for _, x := range gone {
		d, err := x.desired()
		if err != nil {
			return false, nil, err
		}
		removed = append(removed, d)
	}
	for _, d := range removed {
		if _, err := q.ExecContext(ctx, `DELETE FROM fleet.resource_status WHERE organization_id = $1 AND environment_id = $2 AND name = $3`,
			organization, d.Environment, d.Name); err != nil {
			return false, nil, pgerr.Map(err, "forget removed resources")
		}
	}
	return true, removed, nil
}
