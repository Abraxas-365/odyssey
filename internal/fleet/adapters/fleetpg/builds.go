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
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/lib/pq"
)

const buildColumns = `id, organization_id, project_id, environment_id, cluster_id, source_key, source_sha256, source_size,
	image_repository, cache_ref, timeout_seconds, cancelled, final, created_at`

type buildRow struct {
	fleet.DesiredBuild
	TimeoutSeconds int64 `db:"timeout_seconds"`
}

func (r buildRow) build() fleet.DesiredBuild {
	b := r.DesiredBuild
	b.Timeout = time.Duration(r.TimeoutSeconds) * time.Second
	return b
}

// PutBuild stores a new desired build and signals its cluster's gateway in
// the same transaction; a build already stored is left as it is (false).
func (r *Repository) PutBuild(ctx context.Context, b fleet.DesiredBuild) (bool, error) {
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO fleet.desired_builds (id, organization_id, project_id, environment_id, cluster_id, source_key,
		     source_sha256, source_size, image_repository, cache_ref, timeout_seconds)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (id) DO NOTHING`,
		b.ID, b.Organization, b.Project, b.Environment, b.Cluster, b.SourceKey, b.SourceSHA256, b.SourceSize,
		b.ImageRepository, b.CacheRef, int64(b.Timeout/time.Second))
	if err != nil {
		return false, pgerr.Map(err, "put build")
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, pgerr.Map(err, "put build")
	}
	return true, r.notify(ctx, fleet.Signal{Organization: b.Organization, Cluster: b.Cluster, Build: b.ID})
}

// CancelBuild marks the build no longer wanted and signals its cluster
// (false, with the build, when it already was; NotFound when unknown).
func (r *Repository) CancelBuild(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) (fleet.DesiredBuild, bool, error) {
	q := txn.Q(ctx, r.db)
	var row buildRow
	err := q.GetContext(ctx, &row, `SELECT `+buildColumns+` FROM fleet.desired_builds
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.DesiredBuild{}, false, errx.NotFound("build not found")
	}
	if err != nil {
		return fleet.DesiredBuild{}, false, pgerr.Map(err, "cancel build")
	}
	b := row.build()
	if b.Cancelled {
		return b, false, nil
	}
	if _, err := q.ExecContext(ctx, `UPDATE fleet.desired_builds SET cancelled = true, updated_at = now()
		WHERE organization_id = $1 AND id = $2`, organization, id); err != nil {
		return b, false, pgerr.Map(err, "cancel build")
	}
	b.Cancelled = true
	return b, true, r.notify(ctx, fleet.Signal{Organization: organization, Cluster: b.Cluster, Build: id})
}

// FindBuild is a build of the organization, on whichever cluster.
func (r *Repository) FindBuild(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) (fleet.DesiredBuild, error) {
	var row buildRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row, `SELECT `+buildColumns+` FROM fleet.desired_builds
		WHERE organization_id = $1 AND id = $2`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.DesiredBuild{}, errx.NotFound("build not found")
	}
	return row.build(), pgerr.Map(err, "find build")
}

// ListBuilds are the cluster's builds changed after since, oldest first.
func (r *Repository) ListBuilds(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, since time.Time) ([]fleet.DesiredBuild, error) {
	var rows []buildRow
	err := txn.Q(ctx, r.db).SelectContext(ctx, &rows, `SELECT `+buildColumns+` FROM fleet.desired_builds
		WHERE organization_id = $1 AND cluster_id = $2 AND updated_at > $3 ORDER BY created_at, id`, organization, cluster, since)
	if err != nil {
		return nil, pgerr.Map(err, "list builds")
	}
	out := make([]fleet.DesiredBuild, len(rows))
	for i, row := range rows {
		out[i] = row.build()
	}
	return out, nil
}

// FindBuildStatus is what the agent last reported, with the image it
// names (NotFound before any report).
func (r *Repository) FindBuildStatus(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) (fleet.BuildStatus, error) {
	var row struct {
		fleet.BuildStatus
		Repository string `db:"image_repository"`
	}
	err := txn.Q(ctx, r.db).GetContext(ctx, &row,
		`SELECT s.build_id, s.phase, s.image_digest, s.reason, s.message, s.observed_at, d.image_repository
		 FROM fleet.build_status s JOIN fleet.desired_builds d ON d.organization_id = s.organization_id AND d.id = s.build_id
		 WHERE s.organization_id = $1 AND s.build_id = $2`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.BuildStatus{}, errx.NotFound("build status not found")
	}
	if err != nil {
		return fleet.BuildStatus{}, pgerr.Map(err, "find build status")
	}
	s := row.BuildStatus
	if s.Digest != "" {
		s.Image = row.Repository + "@" + s.Digest
	}
	return s, nil
}

// ObserveBuilds upserts the reported statuses of builds desired on this
// cluster, in one statement, while session is the cluster's tunnel. A
// report of another cluster's (or tenant's) build is dropped, and a final
// phase never changes (an agent cannot turn a failure into a success).
func (r *Repository) ObserveBuilds(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, session fleet.Session, statuses []fleet.BuildStatus) (bool, error) {
	n := len(statuses)
	ids, phases, digests, reasons, msgs := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, s := range statuses {
		ids[i], phases[i], digests[i], reasons[i], msgs[i] = s.Build.String(), string(s.Phase), s.Digest, s.Reason, s.Message
	}
	var live bool
	err := txn.Q(ctx, r.db).GetContext(ctx, &live,
		`WITH tunnel AS (
		    SELECT 1 FROM fleet.clusters WHERE organization_id = $1 AND id = $2 AND status = 'active' AND session_id = $3
		 ), reported AS (
		    SELECT DISTINCT ON (r.id) d.id, r.phase, r.digest, r.reason, r.message
		    FROM unnest($4::uuid[], $5::text[], $6::text[], $7::text[], $8::text[]) WITH ORDINALITY AS r(id, phase, digest, reason, message, ord)
		    JOIN fleet.desired_builds d ON d.organization_id = $1 AND d.cluster_id = $2 AND d.id = r.id
		    WHERE EXISTS (SELECT 1 FROM tunnel)
		    ORDER BY r.id, r.ord DESC
		 ), upserted AS (
		    INSERT INTO fleet.build_status (build_id, organization_id, phase, image_digest, reason, message, observed_at)
		    SELECT id, $1, phase, digest, reason, message, now() FROM reported
		    ON CONFLICT (build_id) DO UPDATE SET phase = EXCLUDED.phase, image_digest = EXCLUDED.image_digest,
		        reason = EXCLUDED.reason, message = EXCLUDED.message, observed_at = now()
		    WHERE fleet.build_status.organization_id = $1 AND fleet.build_status.phase NOT IN ('succeeded', 'failed')
		    RETURNING build_id, phase
		 ), finished AS (
		    UPDATE fleet.desired_builds d SET final = true, updated_at = now()
		    FROM upserted u WHERE d.organization_id = $1 AND d.id = u.build_id AND u.phase IN ('succeeded', 'failed') AND NOT d.final
		    RETURNING 1
		 )
		 SELECT EXISTS (SELECT 1 FROM tunnel)`,
		organization, cluster, session, pq.Array(ids), pq.Array(phases), pq.Array(digests), pq.Array(reasons), pq.Array(msgs))
	if err != nil {
		return false, pgerr.Map(err, "observe builds")
	}
	return live, nil
}

// ObserveBuildLog stores the lines of a build desired on this cluster, in
// one statement, while session is the cluster's tunnel: lines already
// stored are skipped (a resend), and the first line that would take the
// log past maxBytes marks it truncated: it and every later line are
// dropped. A line counts its bytes plus one, at least minLine, so the cap
// also bounds the rows. Nothing is stored once the log was archived
// (ObserveLogsArchived).
func (r *Repository) ObserveBuildLog(ctx context.Context, organization identity.OrganizationID, cluster fleet.ClusterID, session fleet.Session, log fleet.BuildLog, maxBytes, minLine int64) (bool, error) {
	n := len(log.Lines)
	seqs, streams, texts := make([]int64, n), make([]string, n), make([]string, n)
	for i, l := range log.Lines {
		seqs[i], streams[i], texts[i] = l.Seq, l.Stream, l.Text
	}
	var live bool
	err := txn.Q(ctx, r.db).GetContext(ctx, &live,
		`WITH tunnel AS (
		    SELECT 1 FROM fleet.clusters WHERE organization_id = $1 AND id = $2 AND status = 'active' AND session_id = $3
		 ), b AS (
		    SELECT id, log_bytes FROM fleet.desired_builds
		    WHERE organization_id = $1 AND cluster_id = $2 AND id = $4 AND NOT log_forgotten AND NOT log_truncated
		      AND EXISTS (SELECT 1 FROM tunnel)
		    FOR UPDATE
		 ), fresh AS (
		    SELECT DISTINCT ON (r.seq) r.seq, r.stream, r.text
		    FROM unnest($5::bigint[], $6::text[], $7::text[]) AS r(seq, stream, text)
		    WHERE NOT EXISTS (SELECT 1 FROM fleet.build_logs x WHERE x.build_id = $4 AND x.seq = r.seq)
		    ORDER BY r.seq
		 ), sized AS (
		    SELECT f.*, greatest(octet_length(f.text) + 1, $9) AS cost FROM fresh f
		 ), totals AS (
		    SELECT s.*, b.log_bytes + sum(s.cost) OVER (ORDER BY s.seq) AS total FROM sized s, b
		 ), inserted AS (
		    INSERT INTO fleet.build_logs (build_id, seq, organization_id, stream, text)
		    SELECT $4, seq, $1, stream, text FROM totals WHERE total <= $8
		    ON CONFLICT DO NOTHING
		    RETURNING greatest(octet_length(text) + 1, $9) AS bytes
		 ), counted AS (
		    UPDATE fleet.desired_builds d
		    SET log_bytes = d.log_bytes + COALESCE((SELECT sum(bytes) FROM inserted), 0),
		        log_truncated = d.log_truncated OR EXISTS (SELECT 1 FROM totals WHERE total > $8)
		    FROM b WHERE d.id = b.id
		    RETURNING 1
		 )
		 SELECT EXISTS (SELECT 1 FROM tunnel)`,
		organization, cluster, session, log.Build, pq.Array(seqs), pq.Array(streams), pq.Array(texts), maxBytes, minLine)
	if err != nil {
		return false, pgerr.Map(err, "observe build log")
	}
	return live, nil
}

// ListBuildLog pages the stored lines of a build (NotFound for an unknown one).
func (r *Repository) ListBuildLog(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID, after int64, limit int) (fleet.LogPage, error) {
	q := txn.Q(ctx, r.db)
	var head struct {
		Truncated bool  `db:"log_truncated"`
		LastSeq   int64 `db:"last_seq"`
	}
	err := q.GetContext(ctx, &head, `SELECT d.log_truncated,
		    COALESCE((SELECT max(seq) FROM fleet.build_logs l WHERE l.build_id = d.id), 0) AS last_seq
		 FROM fleet.desired_builds d WHERE d.organization_id = $1 AND d.id = $2`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.LogPage{}, errx.NotFound("build not found")
	}
	if err != nil {
		return fleet.LogPage{}, pgerr.Map(err, "list build log")
	}
	page := fleet.LogPage{Lines: []fleet.LogLine{}, Truncated: head.Truncated, LastSeq: head.LastSeq}
	err = q.SelectContext(ctx, &page.Lines, `SELECT seq, stream, text FROM fleet.build_logs
		WHERE organization_id = $1 AND build_id = $2 AND seq > $3 ORDER BY seq LIMIT $4`, organization, id, after, limit)
	return page, pgerr.Map(err, "list build log")
}

// ObserveLogsArchived records that the build domain archived the log: the
// stored lines are dropped and later ones are not stored.
func (r *Repository) ObserveLogsArchived(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) error {
	q := txn.Q(ctx, r.db)
	if _, err := q.ExecContext(ctx, `UPDATE fleet.desired_builds SET log_forgotten = true
		WHERE organization_id = $1 AND id = $2`, organization, id); err != nil {
		return pgerr.Map(err, "forget build log")
	}
	_, err := q.ExecContext(ctx, `DELETE FROM fleet.build_logs WHERE organization_id = $1 AND build_id = $2`, organization, id)
	return pgerr.Map(err, "forget build log")
}
