// Package fleetpg is the Postgres implementation of fleet.Repository and of
// fleet.Signals (LISTEN/NOTIFY: a change committed by any process reaches
// the gateways holding the tunnels).
package fleetpg

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Schema is the Postgres schema (and migration owner) of this repository.
const Schema = "fleet"

// Channel is the NOTIFY channel of fleet signals.
const Channel = "odyssey_fleet"

//go:embed migrations/*.up.sql
var migrations embed.FS

// Migrations are this repository's *.up.sql files.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ fleet.Repository = (*Repository)(nil)

var messages = pgerr.Messages{Unique: "a cluster with this name already exists"}

// clusterColumns: connected is computed with the database clock (one clock
// for every replica); apps counts its desired apps (by its index).
var clusterColumns = `id, organization_id, name, status, token_expires_at, certificate_serial,
	certificate_expires_at, agent, created_by, created_at, enrolled_at, last_seen_at, revoked_at,
	(status = 'active' AND session_id IS NOT NULL AND last_seen_at > now() - make_interval(secs => ` + onlineSecs + `)) AS connected,
	(SELECT count(*) FROM fleet.desired_apps d WHERE d.organization_id = clusters.organization_id AND d.cluster_id = clusters.id AND NOT d.deleted) AS apps`

var onlineSecs = strconv.Itoa(int(fleet.OnlineWithin.Seconds()))

type clusterRow struct {
	fleet.Cluster
	Serial    string     `db:"certificate_serial"`
	ExpiresAt *time.Time `db:"certificate_expires_at"`
	Agent     []byte     `db:"agent"`
}

func (r clusterRow) cluster() (fleet.Cluster, error) {
	c := r.Cluster
	c.Certificate = fleet.CertificateInfo{Serial: r.Serial, ExpiresAt: r.ExpiresAt}
	if len(r.Agent) > 0 {
		if err := json.Unmarshal(r.Agent, &c.Agent); err != nil {
			return fleet.Cluster{}, errx.Wrap(err, "decode cluster agent", errx.TypeInternal)
		}
	}
	if c.Agent.Capabilities.APIs == nil {
		c.Agent.Capabilities.APIs = []string{}
	}
	if c.Agent.Capabilities.IngressClasses == nil {
		c.Agent.Capabilities.IngressClasses = []string{}
	}
	if c.Agent.Capabilities.StorageClasses == nil {
		c.Agent.Capabilities.StorageClasses = []string{}
	}
	if c.Agent.Capabilities.ResourceKinds == nil {
		c.Agent.Capabilities.ResourceKinds = []string{}
	}
	return c, nil
}

func (r *Repository) CreateCluster(ctx context.Context, c fleet.Cluster, tokenHash []byte) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.clusters (id, organization_id, name, status, token_hash, token_expires_at, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID, c.Organization, text.Clean(c.Name), c.Status, tokenHash, c.TokenExpiresAt, c.CreatedBy)
	if err = pgerr.MapWith(err, "create cluster", messages); errx.IsType(err, errx.TypeConflict) {
		var e *errx.Error
		if errx.As(err, &e) {
			return e.WithField("name")
		}
	}
	return err
}

func (r *Repository) FindCluster(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) (fleet.Cluster, error) {
	return r.findCluster(ctx, `organization_id = $1 AND id = $2`, "", organization, id)
}

func (r *Repository) FindClusterForUpdate(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) (fleet.Cluster, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return fleet.Cluster{}, errx.Internal("FindClusterForUpdate needs a transaction")
	}
	return r.findCluster(ctx, `organization_id = $1 AND id = $2`, " FOR UPDATE", organization, id)
}

func (r *Repository) FindClusterForShare(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) (fleet.Cluster, error) {
	if _, ok := txn.Tx(ctx); !ok {
		return fleet.Cluster{}, errx.Internal("FindClusterForShare needs a transaction")
	}
	// KEY SHARE conflicts with FOR UPDATE but not with the heartbeats' plain
	// updates
	return r.findCluster(ctx, `organization_id = $1 AND id = $2`, " FOR KEY SHARE", organization, id)
}

func (r *Repository) FindDefaultCluster(ctx context.Context, organization identity.OrganizationID) (fleet.Cluster, error) {
	return r.findCluster(ctx, `organization_id = $1 AND status = 'active' ORDER BY enrolled_at, id LIMIT 1`, "", organization)
}

func (r *Repository) findCluster(ctx context.Context, where, lock string, args ...any) (fleet.Cluster, error) {
	var row clusterRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+clusterColumns+` FROM fleet.clusters WHERE `+where+lock, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Cluster{}, errx.NotFound("cluster not found")
	}
	if err != nil {
		return fleet.Cluster{}, pgerr.MapWith(err, "find cluster", messages)
	}
	return row.cluster()
}

func (r *Repository) ListClusters(ctx context.Context, organization identity.OrganizationID, filter fleet.Filter, page query.Pagination) (query.Paginated[fleet.Cluster], error) {
	where := []string{"organization_id = $1"}
	args := []any{organization}
	if len(filter.Status) > 0 {
		values := make([]string, len(filter.Status))
		for i, v := range filter.Status {
			values[i] = string(v)
		}
		args = append(args, pq.Array(values))
		where = append(where, "status = ANY($"+strconv.Itoa(len(args))+")")
	}
	if pattern := query.EscapeLike(page.Search); pattern != "" {
		args = append(args, pattern)
		where = append(where, "name ILIKE $"+strconv.Itoa(len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := txn.Q(ctx, r.db).GetContext(ctx, &total, `SELECT count(*) FROM fleet.clusters WHERE `+clause, args...); err != nil {
		return query.Paginated[fleet.Cluster]{}, pgerr.MapWith(err, "count clusters", messages)
	}
	var rows []clusterRow
	args = append(args, page.Limit, page.Offset)
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+clusterColumns+` FROM fleet.clusters WHERE `+clause+
		` ORDER BY status = 'revoked', lower(name), id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return query.Paginated[fleet.Cluster]{}, pgerr.MapWith(err, "list clusters", messages)
	}
	items := make([]fleet.Cluster, 0, len(rows))
	for _, row := range rows {
		c, err := row.cluster()
		if err != nil {
			return query.Paginated[fleet.Cluster]{}, err
		}
		items = append(items, c)
	}
	return query.NewPaginated(items, total, page), nil
}

func (r *Repository) SetToken(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, hash []byte, expiresAt time.Time) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET token_hash = $3, token_expires_at = $4
		 WHERE organization_id = $1 AND id = $2 AND status <> 'revoked'`, organization, id, hash, expiresAt)
	return affected(res, err, "set cluster token")
}

func (r *Repository) ConsumeToken(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, hash []byte) (fleet.Cluster, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET token_hash = NULL, token_expires_at = NULL
		 WHERE organization_id = $1 AND id = $2 AND status <> 'revoked'
		   AND token_hash = $3 AND token_expires_at > now()`, organization, id, hash)
	if err := affected(res, err, "consume cluster token"); err != nil {
		return fleet.Cluster{}, err
	}
	return r.FindCluster(ctx, organization, id)
}

func (r *Repository) SetCertificate(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, cert fleet.Issued) error {
	// a new identity: nothing presented before is accepted, and the tunnel
	// of the old key (if any) is superseded
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET previous_serial = '', previous_expires_at = NULL,
		 certificate_serial = $3, certificate_expires_at = $4, certificate_pem = $5, status = 'active',
		 enrolled_at = coalesce(enrolled_at, now()), session_id = NULL
		 WHERE organization_id = $1 AND id = $2 AND status <> 'revoked'`, organization, id, cert.Serial, cert.ExpiresAt, cert.PEM)
	if err := affected(res, err, "set cluster certificate"); err != nil {
		return err
	}
	return r.notify(ctx, fleet.Signal{Organization: organization, Cluster: id, Reenrolled: true})
}

func (r *Repository) RenewCertificate(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, cert fleet.Issued, presented string, previousUntil time.Time) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET previous_serial = $5,
		 previous_expires_at = CASE WHEN $5 = certificate_serial THEN least(certificate_expires_at, $6) ELSE least(previous_expires_at, $6) END,
		 certificate_serial = $3, certificate_expires_at = $4, certificate_pem = $7
		 WHERE organization_id = $1 AND id = $2 AND status = 'active'`,
		organization, id, cert.Serial, cert.ExpiresAt, presented, previousUntil, cert.PEM)
	return affected(res, err, "renew cluster certificate")
}

func (r *Repository) FindCertificate(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) ([]byte, error) {
	var pemBytes []byte
	err := txn.Q(ctx, r.db).GetContext(ctx, &pemBytes,
		`SELECT certificate_pem FROM fleet.clusters
		 WHERE organization_id = $1 AND id = $2 AND status = 'active' AND certificate_pem IS NOT NULL`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errx.NotFound("cluster not found")
	}
	if err != nil {
		return nil, pgerr.Map(err, "find cluster certificate")
	}
	return pemBytes, nil
}

func (r *Repository) FindAcceptedSerials(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) ([]string, error) {
	var row struct {
		Current  string `db:"certificate_serial"`
		Previous string `db:"previous_serial"`
		Valid    bool   `db:"previous_valid"`
	}
	err := txn.Q(ctx, r.db).GetContext(ctx, &row,
		`SELECT certificate_serial, previous_serial, coalesce(previous_expires_at > now(), false) AS previous_valid
		 FROM fleet.clusters WHERE organization_id = $1 AND id = $2 AND status = 'active'`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errx.NotFound("cluster not found")
	}
	if err != nil {
		return nil, pgerr.Map(err, "find cluster serials")
	}
	out := []string{row.Current}
	if row.Previous != "" && row.Valid {
		out = append(out, row.Previous)
	}
	return out, nil
}

func (r *Repository) Revoke(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, revokedBy identity.UserID) error {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET status = 'revoked', revoked_at = now(), revoked_by = $3, token_hash = NULL,
		 token_expires_at = NULL, session_id = NULL
		 WHERE organization_id = $1 AND id = $2 AND status <> 'revoked'`, organization, id, revokedBy)
	if err := affected(res, err, "revoke cluster"); err != nil {
		return err
	}
	return r.notify(ctx, fleet.Signal{Organization: organization, Cluster: id, Revoked: true})
}

func (r *Repository) SetSession(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, session fleet.Session, info fleet.Agent) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return errx.Wrap(err, "encode cluster agent", errx.TypeInternal)
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET session_id = $3, agent = $4, last_seen_at = now()
		 WHERE organization_id = $1 AND id = $2 AND status = 'active'`, organization, id, session, raw)
	return affected(res, err, "set cluster session")
}

func (r *Repository) ObserveHeartbeat(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, session fleet.Session, serial string) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET last_seen_at = now()
		 WHERE organization_id = $1 AND id = $2 AND status = 'active' AND session_id = $3`+acceptsSerial("$4"),
		organization, id, session, serial)
	if err != nil {
		return false, pgerr.Map(err, "cluster heartbeat")
	}
	n, err := res.RowsAffected()
	return n == 1, pgerr.Map(err, "cluster heartbeat")
}

// acceptsSerial is the condition of FindAcceptedSerials on fleet.clusters,
// for the serial in param.
func acceptsSerial(param string) string {
	return ` AND (certificate_serial = ` + param + ` OR (previous_serial = ` + param + ` AND previous_serial <> '' AND previous_expires_at > now()))`
}

func (r *Repository) ObserveDisconnect(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, session fleet.Session) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET session_id = NULL
		 WHERE organization_id = $1 AND id = $2 AND session_id = $3`, organization, id, session)
	return pgerr.Map(err, "cluster disconnect")
}

// ObserveApps upserts the reported statuses of apps desired on this
// cluster, in one statement, while session is the cluster's tunnel. A
// report of an app the cluster does not run is dropped (an agent cannot
// write another cluster's, or another tenant's, status); so is one of a
// revision beyond the desired one (it would hide every real report after
// it). A status never goes back to an older revision. A retired app reported
// "deleted" at (or past) its revision is removed, status first, and returned.
func (r *Repository) ObserveApps(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, session fleet.Session, report fleet.Report) (bool, []fleet.App, error) {
	n := len(report.Apps)
	envs, names, revs, phases, reasons, msgs, replicas, urls, services :=
		make([]string, n), make([]string, n), make([]int64, n), make([]string, n), make([]string, n), make([]string, n), make([]int64, n), make([]string, n), make([]string, n)
	for i, s := range report.Apps {
		envs[i], names[i], revs[i], phases[i] = s.Environment.String(), s.Name, s.ObservedRevision, string(s.Phase)
		reasons[i], msgs[i], replicas[i], urls[i] = string(s.Reason), s.Message, int64(s.ReadyReplicas), s.URL
		svcs := s.Services
		if svcs == nil {
			svcs = []fleet.ServiceStatus{}
		}
		raw, err := json.Marshal(svcs)
		if err != nil {
			return false, nil, errx.Wrap(err, "encode service status", errx.TypeInternal)
		}
		services[i] = string(raw)
	}
	var live bool
	q := txn.Q(ctx, r.db)
	err := q.GetContext(ctx, &live,
		`WITH tunnel AS (
		    SELECT 1 FROM fleet.clusters WHERE organization_id = $1 AND id = $2 AND status = 'active' AND session_id = $3
		 ), reported AS (
		    SELECT DISTINCT ON (r.env, r.name) d.environment_id, d.name, d.organization_id, d.cluster_id,
		           r.rev, r.phase, r.reason, r.message, r.replicas, r.url, r.services
		    FROM unnest($4::uuid[], $5::text[], $6::bigint[], $7::text[], $8::text[], $9::text[], $10::int[], $11::text[], $12::jsonb[])
		         AS r(env, name, rev, phase, reason, message, replicas, url, services)
		    JOIN fleet.desired_apps d ON d.environment_id = r.env AND d.name = r.name
		    WHERE d.organization_id = $1 AND d.cluster_id = $2 AND r.rev <= d.revision AND r.phase <> 'deleted'
		    ORDER BY r.env, r.name, r.rev DESC
		 ), upserted AS (
		    INSERT INTO fleet.app_status (environment_id, name, organization_id, cluster_id, observed_revision, phase,
		        reason, message, ready_replicas, url, services, observed_at)
		    SELECT environment_id, name, organization_id, cluster_id, rev, phase, reason, message, replicas, url, services, now()
		    FROM reported
		    WHERE EXISTS (SELECT 1 FROM tunnel)
		    ON CONFLICT (environment_id, name) DO UPDATE SET
		        observed_revision = EXCLUDED.observed_revision, phase = EXCLUDED.phase, reason = EXCLUDED.reason,
		        message = EXCLUDED.message, ready_replicas = EXCLUDED.ready_replicas, url = EXCLUDED.url,
		        services = EXCLUDED.services, observed_at = now()
		    WHERE fleet.app_status.organization_id = $1 AND fleet.app_status.cluster_id = $2
		      AND fleet.app_status.observed_revision <= EXCLUDED.observed_revision
		    RETURNING 1
		 )
		 SELECT EXISTS (SELECT 1 FROM tunnel)`,
		organization, id, session, pq.Array(envs), pq.Array(names), pq.Array(revs), pq.Array(phases), pq.Array(reasons),
		pq.Array(msgs), pq.Array(replicas), pq.Array(urls), pq.Array(services))
	if err != nil {
		return false, nil, pgerr.Map(err, "observe apps")
	}
	if !live {
		return false, nil, nil
	}
	// The status goes first (it references the desired app); both only for a
	// retired app whose retirement the cluster carried out. The retired rows
	// are locked first: a deployment reviving one (PutApp) meanwhile would
	// otherwise lose its status while its desired app stays.
	const gone = `d.organization_id = $1 AND d.cluster_id = $2 AND d.environment_id = r.env AND d.name = r.name
		   AND d.deleted AND r.phase = 'deleted' AND r.rev >= d.revision`
	reported := `unnest($3::uuid[], $4::text[], $5::bigint[], $6::text[]) AS r(env, name, rev, phase)`
	args := []any{organization, id, pq.Array(envs), pq.Array(names), pq.Array(revs), pq.Array(phases)}
	if _, err := q.ExecContext(ctx,
		`SELECT 1 FROM fleet.desired_apps d, `+reported+` WHERE `+gone+` FOR UPDATE OF d`, args...); err != nil {
		return false, nil, pgerr.Map(err, "forget retired apps")
	}
	if _, err := q.ExecContext(ctx,
		`DELETE FROM fleet.app_status st USING fleet.desired_apps d, `+reported+`
		 WHERE st.environment_id = d.environment_id AND st.name = d.name AND st.organization_id = $1 AND `+gone, args...); err != nil {
		return false, nil, pgerr.Map(err, "forget retired apps")
	}
	var rows []appRow
	if err := q.SelectContext(ctx, &rows,
		`DELETE FROM fleet.desired_apps d USING `+reported+` WHERE `+gone+`
		 RETURNING `+prefixed("d.", appColumns), args...); err != nil {
		return false, nil, pgerr.Map(err, "forget retired apps")
	}
	removed := make([]fleet.App, 0, len(rows))
	for _, row := range rows {
		a, err := row.app()
		if err != nil {
			return false, nil, err
		}
		removed = append(removed, a)
	}
	return true, removed, nil
}

// prefixed qualifies a column list with a table alias.
func prefixed(alias, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

func (r *Repository) FindPlacement(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (fleet.ClusterID, error) {
	var id fleet.ClusterID
	err := txn.Q(ctx, r.db).GetContext(ctx, &id,
		`SELECT cluster_id FROM fleet.placements WHERE organization_id = $1 AND environment_id = $2`, organization, environment)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.ClusterID{}, errx.NotFound("environment not placed")
	}
	return id, pgerr.Map(err, "find placement")
}

func (r *Repository) ListPlacementsOf(ctx context.Context, organization identity.OrganizationID, envs []project.EnvironmentID) (map[project.EnvironmentID]fleet.ClusterID, error) {
	strs := make([]string, len(envs))
	for i, e := range envs {
		strs[i] = e.String()
	}
	var rows []struct {
		Environment project.EnvironmentID `db:"environment_id"`
		Cluster     fleet.ClusterID       `db:"cluster_id"`
	}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows,
		`SELECT environment_id, cluster_id FROM fleet.placements WHERE organization_id = $1 AND environment_id = ANY($2::uuid[])`,
		organization, pq.Array(strs))
	if err != nil {
		return nil, pgerr.Map(err, "list placements")
	}
	out := make(map[project.EnvironmentID]fleet.ClusterID, len(rows))
	for _, row := range rows {
		out[row.Environment] = row.Cluster
	}
	return out, nil
}

func (r *Repository) Place(ctx context.Context, organization identity.OrganizationID, p project.ID, environment project.EnvironmentID, cluster fleet.ClusterID) (fleet.ClusterID, error) {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.placements (environment_id, organization_id, project_id, cluster_id)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (environment_id) DO NOTHING`, environment, organization, p, cluster)
	if err != nil {
		return fleet.ClusterID{}, pgerr.Map(err, "place environment")
	}
	return r.FindPlacement(ctx, organization, environment)
}

func (r *Repository) MovePlacement(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, from, to fleet.ClusterID) (int, error) {
	q := txn.Q(ctx, r.db)
	// what the old cluster reported no longer describes anything, and values
	// sealed to its key cannot be opened by the new one (loaded again there)
	if _, err := q.ExecContext(ctx,
		`DELETE FROM fleet.app_status WHERE organization_id = $1 AND environment_id = $2 AND cluster_id = $3`,
		organization, environment, from); err != nil {
		return 0, pgerr.Map(err, "move placement")
	}
	if _, err := q.ExecContext(ctx,
		`DELETE FROM fleet.secret_deliveries WHERE organization_id = $1 AND environment_id = $2 AND cluster_id = $3`,
		organization, environment, from); err != nil {
		return 0, pgerr.Map(err, "move placement")
	}
	res, err := q.ExecContext(ctx,
		`UPDATE fleet.placements SET cluster_id = $4, created_at = now()
		 WHERE organization_id = $1 AND environment_id = $2 AND cluster_id = $3`, organization, environment, from, to)
	if err := affected(res, err, "move placement"); err != nil {
		return 0, err
	}
	var names []string
	if err := q.SelectContext(ctx, &names,
		`UPDATE fleet.desired_apps SET cluster_id = $4, updated_at = now()
		 WHERE organization_id = $1 AND environment_id = $2 AND cluster_id = $3 RETURNING name`,
		organization, environment, from, to); err != nil {
		return 0, pgerr.Map(err, "move placement")
	}
	for _, name := range names { // the new cluster's gateway sends them
		if err := r.notify(ctx, fleet.Signal{Organization: organization, Cluster: to, Environment: environment, Name: name}); err != nil {
			return 0, err
		}
	}
	// Resources move with their apps (their data does not: the new cluster
	// prepares them empty).
	if _, err := q.ExecContext(ctx,
		`DELETE FROM fleet.resource_status WHERE organization_id = $1 AND environment_id = $2 AND cluster_id = $3`,
		organization, environment, from); err != nil {
		return 0, pgerr.Map(err, "move placement")
	}
	var resources []string
	if err := q.SelectContext(ctx, &resources,
		`UPDATE fleet.desired_resources SET cluster_id = $4, updated_at = now()
		 WHERE organization_id = $1 AND environment_id = $2 AND cluster_id = $3 RETURNING name`,
		organization, environment, from, to); err != nil {
		return 0, pgerr.Map(err, "move placement")
	}
	for _, name := range resources {
		if err := r.notify(ctx, fleet.Signal{Organization: organization, Cluster: to, Environment: environment, Name: name, Resource: true}); err != nil {
			return 0, err
		}
	}
	return len(names), nil
}

func (r *Repository) ListPlacements(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID) ([]project.EnvironmentID, error) {
	ids := []project.EnvironmentID{}
	err := txn.Q(ctx, r.db).SelectContext(ctx, &ids,
		`SELECT environment_id FROM fleet.placements WHERE organization_id = $1 AND cluster_id = $2 ORDER BY environment_id`,
		organization, cluster)
	return ids, pgerr.Map(err, "list placements")
}

// spec is the stored form of an app's desired state.
type spec struct {
	Image      string              `json:"image"`
	Port       int                 `json:"port"`
	HealthPath string              `json:"health_path"`
	Replicas   int                 `json:"replicas"`
	Env        map[string]string   `json:"env"`
	Labels     map[string]string   `json:"labels"`
	Access     string              `json:"access,omitempty"`
	Secrets    []fleet.AppSecret   `json:"secrets,omitempty"`
	Resources  []fleet.ResourceRef `json:"resources,omitempty"`
	AI         *fleet.AIRef        `json:"ai,omitempty"`
	Services   []fleet.AppService  `json:"services,omitempty"`
}

func (r *Repository) PutApp(ctx context.Context, a fleet.App) (bool, error) {
	raw, err := json.Marshal(spec{Image: a.Image, Port: a.Port, HealthPath: a.HealthPath, Replicas: a.Replicas, Env: a.Env, Labels: a.Labels,
		Access: a.Access, Secrets: a.Secrets, Resources: a.Resources, AI: a.AI, Services: a.Services})
	if err != nil {
		return false, errx.Wrap(err, "encode app", errx.TypeInternal)
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.desired_apps (environment_id, name, organization_id, project_id, cluster_id, revision, spec)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (environment_id, name) DO UPDATE SET revision = EXCLUDED.revision, spec = EXCLUDED.spec, deleted = false, updated_at = now()
		 WHERE fleet.desired_apps.organization_id = EXCLUDED.organization_id
		   AND fleet.desired_apps.cluster_id = EXCLUDED.cluster_id
		   AND fleet.desired_apps.revision < EXCLUDED.revision`,
		a.Environment, a.Name, a.Organization, a.Project, a.Cluster, a.Revision, raw)
	if err != nil {
		return false, pgerr.Map(err, "put app")
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, pgerr.Map(err, "put app")
	}
	return true, r.notify(ctx, fleet.Signal{Organization: a.Organization, Cluster: a.Cluster, Environment: a.Environment, Name: a.Name})
}

// RetireApp keeps the app's last spec (the agent needs only its name) and
// raises its revision, so neither an older deployment nor an older
// retirement wins late; a later deployment revives it (PutApp).
func (r *Repository) RetireApp(ctx context.Context, ret fleet.Retirement) (fleet.App, bool, error) {
	q := txn.Q(ctx, r.db)
	var row appRow
	err := q.GetContext(ctx, &row,
		`UPDATE fleet.desired_apps SET deleted = true, revision = $5, updated_at = now()
		 WHERE organization_id = $1 AND project_id = $2 AND environment_id = $3 AND name = $4 AND revision < $5
		 RETURNING `+appColumns, ret.Organization, ret.Project, ret.Environment, ret.Name, ret.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.App{}, false, nil
	}
	if err != nil {
		return fleet.App{}, false, pgerr.Map(err, "retire app")
	}
	a, err := row.app()
	if err != nil {
		return fleet.App{}, false, err
	}
	// Values still on their way to it would only be stored for nothing.
	if _, err := q.ExecContext(ctx,
		`DELETE FROM fleet.secret_deliveries WHERE organization_id = $1 AND environment_id = $2 AND app = $3`,
		ret.Organization, ret.Environment, ret.Name); err != nil {
		return fleet.App{}, false, pgerr.Map(err, "retire app")
	}
	return a, true, r.notify(ctx, fleet.Signal{Organization: a.Organization, Cluster: a.Cluster, Environment: a.Environment, Name: a.Name})
}

const appColumns = `environment_id, name, organization_id, project_id, cluster_id, revision, spec, deleted, updated_at`

type appRow struct {
	fleet.App
	Spec []byte `db:"spec"`
}

func (r appRow) app() (fleet.App, error) {
	var s spec
	if err := json.Unmarshal(r.Spec, &s); err != nil {
		return fleet.App{}, errx.Wrap(err, "decode app", errx.TypeInternal)
	}
	a := r.App
	a.Image, a.Port, a.HealthPath, a.Replicas, a.Env, a.Labels, a.Access = s.Image, s.Port, s.HealthPath, s.Replicas, s.Env, s.Labels, s.Access
	a.Secrets = s.Secrets
	if a.Secrets == nil {
		a.Secrets = []fleet.AppSecret{}
	}
	a.Resources, a.AI, a.Services = s.Resources, s.AI, s.Services
	if a.Resources == nil {
		a.Resources = []fleet.ResourceRef{}
	}
	return a, nil
}

func (r *Repository) ListApps(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID) ([]fleet.App, error) {
	var rows []appRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+appColumns+` FROM fleet.desired_apps
		WHERE organization_id = $1 AND cluster_id = $2 ORDER BY environment_id, name`, organization, cluster)
	if err != nil {
		return nil, pgerr.Map(err, "list apps")
	}
	out := make([]fleet.App, 0, len(rows))
	for _, row := range rows {
		a, err := row.app()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *Repository) FindApp(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, environment project.EnvironmentID, name string) (fleet.App, error) {
	var row appRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+appColumns+` FROM fleet.desired_apps
		WHERE organization_id = $1 AND cluster_id = $2 AND environment_id = $3 AND name = $4`, organization, cluster, environment, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.App{}, errx.NotFound("app not found")
	}
	if err != nil {
		return fleet.App{}, pgerr.Map(err, "find app")
	}
	return row.app()
}

func (r *Repository) FindAppStatus(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (fleet.AppStatus, error) {
	var row struct {
		fleet.AppStatus
		Services []byte `db:"services"`
	}
	err := txn.Q(ctx, r.db).GetContext(ctx, &row,
		`SELECT environment_id, name, observed_revision, phase, reason, message, ready_replicas, url, services, observed_at
		 FROM fleet.app_status WHERE organization_id = $1 AND environment_id = $2 AND name = $3`, organization, environment, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.AppStatus{}, errx.NotFound("app status not found")
	}
	if err != nil {
		return fleet.AppStatus{}, pgerr.Map(err, "find app status")
	}
	s := row.AppStatus
	if err := json.Unmarshal(row.Services, &s.Services); err != nil {
		return fleet.AppStatus{}, errx.Wrap(err, "decode service status", errx.TypeInternal)
	}
	return s, nil
}

// notify is delivered at commit (Postgres holds NOTIFY until then), so a
// gateway never reads a change that was rolled back.
func (r *Repository) notify(ctx context.Context, s fleet.Signal) error {
	_, err := txn.Q(ctx, r.db).ExecContext(ctx, `SELECT pg_notify($1, $2)`, Channel, encodeSignal(s))
	return pgerr.Map(err, "notify fleet")
}

// Signals are "<org> <cluster> revoked", "<org> <cluster> reenrolled",
// "<org> <cluster> app <env> <name>", "<org> <cluster> build <id>" or
// "<org> <cluster> secret <delivery id>" or "<org> <cluster> resource <env> <name>".
func encodeSignal(s fleet.Signal) string {
	switch {
	case s.Revoked:
		return s.Organization.String() + " " + s.Cluster.String() + " revoked"
	case s.Reenrolled:
		return s.Organization.String() + " " + s.Cluster.String() + " reenrolled"
	case s.Policy:
		return s.Organization.String() + " " + s.Cluster.String() + " policy"
	case !s.Build.IsZero():
		return s.Organization.String() + " " + s.Cluster.String() + " build " + s.Build.String()
	case !s.Secret.IsZero():
		return s.Organization.String() + " " + s.Cluster.String() + " secret " + s.Secret.String()
	case s.Resource:
		return s.Organization.String() + " " + s.Cluster.String() + " resource " + s.Environment.String() + " " + s.Name
	}
	return s.Organization.String() + " " + s.Cluster.String() + " app " + s.Environment.String() + " " + s.Name
}

func decodeSignal(raw string) (fleet.Signal, bool) {
	f := strings.Fields(raw)
	if len(f) < 3 {
		return fleet.Signal{}, false
	}
	org, err := identity.ParseOrganizationID(f[0])
	if err != nil {
		return fleet.Signal{}, false
	}
	cluster, err := fleet.ParseClusterID(f[1])
	if err != nil {
		return fleet.Signal{}, false
	}
	s := fleet.Signal{Organization: org, Cluster: cluster}
	switch {
	case f[2] == "revoked" && len(f) == 3:
		s.Revoked = true
	case f[2] == "reenrolled" && len(f) == 3:
		s.Reenrolled = true
	case f[2] == "policy" && len(f) == 3:
		s.Policy = true
	case f[2] == "app" && len(f) == 5:
		if s.Environment, err = project.ParseEnvironmentID(f[3]); err != nil {
			return fleet.Signal{}, false
		}
		s.Name = f[4]
	case f[2] == "resource" && len(f) == 5:
		if s.Environment, err = project.ParseEnvironmentID(f[3]); err != nil {
			return fleet.Signal{}, false
		}
		s.Name, s.Resource = f[4], true
	case f[2] == "build" && len(f) == 4:
		if s.Build, err = fleet.ParseBuildID(f[3]); err != nil {
			return fleet.Signal{}, false
		}
	case f[2] == "secret" && len(f) == 4:
		if s.Secret, err = fleet.ParseSecretDeliveryID(f[3]); err != nil {
			return fleet.Signal{}, false
		}
	default:
		return fleet.Signal{}, false
	}
	return s, true
}

// affected maps "no row matched" to NotFound.
func affected(res sql.Result, err error, op string) error {
	if err != nil {
		return pgerr.MapWith(err, op, messages)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return pgerr.Map(err, op)
	}
	if n == 0 {
		return errx.NotFound("cluster not found")
	}
	return nil
}
