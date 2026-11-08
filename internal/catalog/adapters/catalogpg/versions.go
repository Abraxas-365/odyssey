package catalogpg

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgerr"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

const versionColumns = `organization_id, recipe, version, kind, title, description, engine, spec, created_by, created_at`

type versionRow struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Recipe       string                  `db:"recipe"`
	Version      int                     `db:"version"`
	Kind         string                  `db:"kind"`
	Title        string                  `db:"title"`
	Description  string                  `db:"description"`
	Engine       string                  `db:"engine"`
	Spec         []byte                  `db:"spec"`
	CreatedBy    identity.UserID         `db:"created_by"`
	CreatedAt    time.Time               `db:"created_at"`
}

// Publish inserts the version (title and description Clean, as validated;
// a zero CreatedAt is now); the table only ever takes inserts (triggers
// refuse updates, deletes and truncation).
func (r *Repository) Publish(ctx context.Context, o catalog.OrgRecipe) error {
	spec, err := json.Marshal(o.Spec)
	if err != nil {
		return pgerr.Map(err, "encode recipe spec")
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	_, err = txn.Q(ctx, r.db).ExecContext(ctx,
		`INSERT INTO catalog.recipe_versions (`+versionColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		o.Organization, o.Recipe, o.Version, string(o.Kind), text.Clean(o.Title), text.Clean(o.Description), o.Engine, spec, o.CreatedBy, o.CreatedAt)
	if err != nil {
		return pgerr.MapWith(err, "publish recipe version", pgerr.Messages{
			Unique: "La versión " + o.Recipe + "@" + strconv.Itoa(o.Version) + " ya existe: una versión publicada no cambia.",
		})
	}
	return nil
}

func (r *Repository) Published(ctx context.Context, org identity.OrganizationID) ([]catalog.OrgRecipe, error) {
	var rows []versionRow
	if err := txn.Q(ctx, r.db).SelectContext(ctx, &rows,
		`SELECT `+versionColumns+` FROM catalog.recipe_versions WHERE organization_id = $1 ORDER BY recipe, version`, org); err != nil {
		return nil, pgerr.Map(err, "list published recipe versions")
	}
	out := make([]catalog.OrgRecipe, 0, len(rows))
	for _, x := range rows {
		o := catalog.OrgRecipe{Organization: x.Organization, Recipe: x.Recipe, Version: x.Version, Kind: resourcekind.Kind(x.Kind),
			Title: x.Title, Description: x.Description, Engine: x.Engine, CreatedBy: x.CreatedBy, CreatedAt: x.CreatedAt}
		var spec resourcespec.Spec
		if err := json.Unmarshal(x.Spec, &spec); err != nil {
			return nil, errx.Wrap(err, "decode recipe spec of "+x.Recipe, errx.TypeInternal)
		}
		o.Spec = spec
		out = append(out, o)
	}
	return out, nil
}
