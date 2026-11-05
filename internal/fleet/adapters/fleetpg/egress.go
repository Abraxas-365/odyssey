package fleetpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

type egressRow struct {
	Egress     []byte     `db:"egress"`
	Generation int64      `db:"policy_generation"`
	Acked      *int64     `db:"policy_acked_generation"`
	Error      string     `db:"policy_error"`
	AckedAt    *time.Time `db:"policy_acked_at"`
	Warnings   []byte     `db:"policy_warnings"`
}

func (r egressRow) setting() (fleet.EgressSetting, error) {
	var e fleet.Egress
	if err := json.Unmarshal(r.Egress, &e); err != nil {
		return fleet.EgressSetting{}, errx.Wrap(err, "decode cluster egress", errx.TypeInternal)
	}
	if e.Mode == "" {
		e.Mode = fleet.EgressInternet
	}
	if e.Allow == nil {
		e.Allow = []string{}
	}
	warnings := []string{}
	if len(r.Warnings) > 0 {
		if err := json.Unmarshal(r.Warnings, &warnings); err != nil {
			return fleet.EgressSetting{}, errx.Wrap(err, "decode cluster egress warnings", errx.TypeInternal)
		}
	}
	return fleet.EgressSetting{Egress: e, Generation: r.Generation, Acknowledged: r.Acked, Error: r.Error, AcknowledgedAt: r.AckedAt, Warnings: warnings}, nil
}

func (r *Repository) FindEgress(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) (fleet.EgressSetting, error) {
	var row egressRow
	err := txn.Q(ctx, r.db).GetContext(ctx, &row,
		`SELECT egress, policy_generation, policy_acked_generation, policy_error, policy_acked_at, policy_warnings
		 FROM fleet.clusters WHERE organization_id = $1 AND id = $2`, organization, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.EgressSetting{}, errx.NotFound("cluster not found")
	}
	if err != nil {
		return fleet.EgressSetting{}, pgerr.Map(err, "find egress")
	}
	return row.setting()
}

func (r *Repository) PutEgress(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, egress fleet.Egress) (int64, error) {
	raw, err := json.Marshal(egress)
	if err != nil {
		return 0, errx.Wrap(err, "encode cluster egress", errx.TypeInternal)
	}
	var generation int64
	err = txn.Q(ctx, r.db).GetContext(ctx, &generation,
		`UPDATE fleet.clusters SET egress = $3, policy_generation = policy_generation + 1
		 WHERE organization_id = $1 AND id = $2 AND status <> 'revoked'
		 RETURNING policy_generation`, organization, id, raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errx.NotFound("cluster not found")
	}
	if err != nil {
		return 0, pgerr.Map(err, "put egress")
	}
	return generation, r.notify(ctx, fleet.Signal{Organization: organization, Cluster: id, Policy: true})
}

func (r *Repository) ObservePolicy(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, session fleet.Session, ack fleet.PolicyAck) (bool, error) {
	warnings, err := json.Marshal(append([]string{}, ack.Warnings...))
	if err != nil {
		return false, errx.Wrap(err, "encode policy warnings", errx.TypeInternal)
	}
	res, err := txn.Q(ctx, r.db).ExecContext(ctx,
		`UPDATE fleet.clusters SET policy_acked_generation = $4, policy_error = $5, policy_acked_at = now(), policy_warnings = $6
		 WHERE organization_id = $1 AND id = $2 AND status = 'active' AND session_id = $3
		   AND $4 <= policy_generation
		   AND (policy_acked_generation IS NULL OR policy_acked_generation <= $4)`,
		organization, id, session, ack.Generation, ack.Error, warnings)
	if err != nil {
		return false, pgerr.Map(err, "observe policy")
	}
	n, err := res.RowsAffected()
	return n == 1, pgerr.Map(err, "observe policy")
}
