package project_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// TestEventContracts pins each payload's JSON (testdata/events): consumers
// (audit, other modules, external subscribers) rely on it.
func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user := identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	id := project.MustParseID("00000000-0000-4000-8000-0000000000aa")
	name, desc := " Visitas ", "Registro de visitas"
	p := project.Project{ID: id, Name: "Visitas a clientes", Slug: "visitas-a-clientes"}
	for _, e := range []event.Event{
		project.NewCreated(org, user, id, p.Name, p.Slug),
		project.NewUpdated(org, user, p, project.Update{Name: &name, Description: &desc}),
		project.NewArchived(org, user, p),
		project.NewEnvironmentCreated(org, user, project.Environment{
			ID: project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000bb"), Project: id,
			Slug: project.ProductionSlug, Kind: project.KindProduction,
		}, p.Name),
	} {
		eventtest.Golden(t, e)
	}
}
