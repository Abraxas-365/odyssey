package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/fleet/adapters/fleetca"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource/adapters/resourcemcp"
)

// TestMCPCatalog: stage 1a cut 5 with the real wiring. list_catalog shows an
// assistant what the app can ask for — the recipes its tier is offered and
// its cluster can honour, with IT's limits and pre-approved range — and
// create_resource goes through the same catalog and rules as the REST API:
// inside the pre-approved range it is created at once, outside it is held for
// IT (nothing created), and outside the hard limits it is refused with a
// next_step that points back at list_catalog.
func TestMCPCatalog(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := fleetca.Generate("test fleet CA", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := fleetca.Parse(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Authority: ca, Gateway: "localhost:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })

	org := identity.NewOrganizationID()
	user := identity.MustParseUserID("11111111-1111-4111-8111-111111111111")
	id, err := platform.Projects.Commands.Create(ctx, org, project.Create{Name: "Portal", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.Projects.Commands.SetTier(ctx, org, id, project.SetTier{Tier: project.TierInternal, UpdatedBy: user}); err != nil {
		t.Fatal(err)
	}
	// IT pre-approves basic databases up to 10 GiB (the limits stay 1–100).
	if _, err := platform.Catalog.Commands.UpdateOffering(ctx, org, catalog.PostgresBasic, catalog.UpdateOffering{UpdatedBy: user,
		OfferingDraft: catalog.OfferingDraft{Version: 1, Active: true, Tiers: []project.Tier{project.TierSandbox, project.TierInternal, project.TierProduction},
			Defaults:    []project.Tier{project.TierSandbox, project.TierInternal, project.TierProduction},
			Preapproved: map[string]catalog.Range{catalog.ParamStorage: {Max: 10}}}}); err != nil {
		t.Fatal(err)
	}

	perms := []string{permission.ProjectsRead, permission.ProjectsWrite}
	caller := mcpx.Principal{Organization: org, User: user, Client: "https://claude.ai/oauth/claude-code-client-metadata", Permissions: perms}
	h := bootstrap.NewMCPHandler(platform, config.Config{ConsoleURL: "http://localhost:5178"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+mcpx.VerifiedBearer)
		h.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), caller)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "catalog", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any, out any) map[string]string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError {
			var body struct{ Error map[string]string }
			if err := json.Unmarshal([]byte(text), &body); err != nil {
				t.Fatal(err)
			}
			return body.Error
		}
		if err := json.Unmarshal([]byte(text), out); err != nil {
			t.Fatal(err)
		}
		return nil
	}

	// No cluster yet: nothing can be asked for, and the assistant is told why.
	var cat resourcemcp.ListCatalogOut
	if e := call("list_catalog", map[string]any{"app": "portal"}, &cat); e != nil {
		t.Fatal(e)
	}
	if len(cat.Recipes) != 0 || len(cat.Unavailable) == 0 || !strings.Contains(cat.Unavailable[0].Reason, "clúster") {
		t.Fatalf("no cluster: %+v", cat)
	}

	// A cluster that prepares the three kinds and announces no recipe spec.
	created, err := platform.Fleet.Commands.Create(ctx, org, fleet.Create{Name: "lima", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	en, err := platform.Fleet.Agents.Enroll(ctx, created.Token.Value, csrFor(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.Fleet.Agents.Connect(ctx, fleet.AgentRef{Organization: org, Cluster: en.Cluster, Serial: en.Certificate.Serial},
		fleet.NewSession(), fleet.Agent{Version: "test", Capabilities: fleet.Capabilities{ResourceKinds: []string{"postgres", "redis", "files"}}},
		"10.0.0.1"); err != nil {
		t.Fatal(err)
	}

	cat = resourcemcp.ListCatalogOut{}
	if e := call("list_catalog", map[string]any{"app": "portal"}, &cat); e != nil {
		t.Fatal(e)
	}
	got := map[string]resourcemcp.Offer{}
	for _, r := range cat.Recipes {
		got[r.Recipe] = r
	}
	pg, ok := got[catalog.PostgresBasic]
	if cat.Tier != "internal" || !cat.CanCreate || !ok || !pg.Default || pg.Approval != "instant" ||
		pg.Params[0].Name != catalog.ParamStorage || pg.Params[0].PreapprovedMax != 10 || pg.Params[0].Max != 100 {
		t.Fatalf("catalog %+v", cat)
	}
	if _, offered := got[catalog.PostgresProduction]; offered {
		t.Fatalf("postgres-produccion is production-only and needs a modern agent: %+v", cat.Recipes)
	}
	if len(pg.Variables) == 0 || pg.Variables[0].Name != "DATABASE_URL" {
		t.Fatalf("variables %+v", pg.Variables)
	}

	// Inside the pre-approved range: created at once, pinned to its recipe.
	var out resourcemcp.CreateDatabaseOut
	if e := call("create_resource", map[string]any{"app": "portal", "params": map[string]int{"storage_gi": 5}, "connect": false}, &out); e != nil {
		t.Fatal(e)
	}
	if out.Status != "" || out.Resource.Name != "db" || out.Resource.StorageGi != 5 || out.Resource.Connected {
		t.Fatalf("created %+v", out)
	}
	envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, id, project.EnvironmentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	envID := envs[0].ID
	stored, err := platform.Resources.Queries.Find(ctx, org, envID, "db")
	if err != nil || stored.Recipe != catalog.PostgresBasic || stored.RecipeVersion != 1 {
		t.Fatalf("stored %+v %v", stored, err)
	}

	// Outside it: held for IT, nothing created.
	out = resourcemcp.CreateDatabaseOut{}
	if e := call("create_database", map[string]any{"app": "portal", "name": "big", "storage_gi": 40}, &out); e != nil {
		t.Fatal(e)
	}
	if out.Status != "pending_approval" || out.RequestID == "" {
		t.Fatalf("held %+v", out)
	}
	if _, err := platform.Resources.Queries.Find(ctx, org, envID, "big"); err == nil {
		t.Fatal("a held creation created the resource")
	}

	// Outside the hard limits: refused, pointing at list_catalog.
	e := call("create_resource", map[string]any{"app": "portal", "name": "huge", "params": map[string]int{"storage_gi": 500}}, &out)
	if e["code"] != catalog.CodeOutOfLimits || !strings.Contains(e["next_step"], "list_catalog") {
		t.Fatalf("out of limits: %v", e)
	}
}
