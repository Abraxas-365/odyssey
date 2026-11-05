package bootstrap_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
	"github.com/Abraxas-365/odyssey/internal/release/releasesvc"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// TestAttentionForIT: what TI watches over (3c). An internal app goes live
// with critical vulnerabilities (the scan rule is for production) and shows
// in the inventory with its scan; a production app's publication is refused
// and shows as failed; a held request waits for TI's decision and, once
// rejected, is on its requester's list.
func TestAttentionForIT(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	runner := buildfake.New(buildfake.Options{Scan: func(b build.Build) build.ScanObservation {
		return build.ScanObservation{Phase: build.ScanDone, Scan: build.Scan{Digest: build.DigestOf(b.Image), Critical: 2, High: 5, Tool: "trivy test"}}
	}})
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		Cluster:  cluster, Runner: runner,
		Build:   buildsvc.Options{PollEvery: 20 * time.Millisecond},
		Release: releasesvc.Options{PollEvery: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	wctx, stop := context.WithCancel(ctx)
	worked := make(chan error, 1)
	go func() {
		worked <- bootstrap.Work(wctx, platform, config.Worker{}, outboxpg.Options{PollInterval: 20 * time.Millisecond})
	}()
	defer func() {
		stop()
		if err := <-worked; err != nil {
			t.Errorf("worker: %v", err)
		}
	}()
	app := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	org := identity.NewOrganizationID()
	switchOff(t, platform, org, policy.BuiltinProductionDeploy)
	dev := []string{permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate, permission.AccessPublic}
	builder := httpxtest.TokenAs("7b1f2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c01", org, dev...)
	itToken := httpxtest.TokenAs(itUser, org, permission.ProjectsRead, permission.InventoryRead, permission.ApprovalsDecide, permission.ProjectsTier)

	type created struct{ base, id string }
	newApp := func(name, tier string) created {
		t.Helper()
		res := checked(t, app, http.MethodPost, "/api/v1/projects", builder, map[string]any{"name": name})
		httpxtest.Want(t, res, http.StatusCreated)
		proj := httpxtest.JSON[project.Project](t, res)
		res = checked(t, app, http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/environments", builder, nil)
		env := httpxtest.JSON[query.Paginated[project.Environment]](t, res).Items[0]
		if tier != "internal" {
			httpxtest.Want(t, checked(t, app, http.MethodPut, "/api/v1/projects/"+proj.ID.String()+"/tier", itToken, map[string]any{"tier": tier}), http.StatusOK)
		}
		return created{base: "/api/v1/projects/" + proj.ID.String() + "/environments/" + env.ID.String(), id: proj.ID.String()}
	}
	publish := func(c created, marker string) release.Deployment {
		t.Helper()
		res := upload(t, app, c.base+"/deployments", builder, configPart(t, map[string]any{"port": 3000, "health_path": "/"}), part{name: "source", file: "x.tar.gz", body: folder(t, marker)})
		httpxtest.Want(t, res, http.StatusAccepted)
		d := httpxtest.JSON[release.Deployment](t, res)
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, c.base+"/deployments/"+d.ID.String(), builder, nil)
			if d = httpxtest.JSON[release.Deployment](t, res); d.Status.Finished() {
				return d
			}
		}
		t.Fatalf("deployment never finished: %s", d.Status)
		return d
	}
	attention := func(token string) inventory.Attention {
		t.Helper()
		res := checked(t, app, http.MethodGet, "/api/v1/attention", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[inventory.Attention](t, res)
	}
	has := func(a inventory.Attention, kind inventory.Kind, scope inventory.Scope, appID string) bool {
		return slices.ContainsFunc(a.Items, func(i inventory.Item) bool {
			return i.Kind == kind && i.Scope == scope && (appID == "" || i.App != nil && i.App.String() == appID)
		})
	}

	internal := newApp("Interna", "internal")
	if d := publish(internal, "a"); d.Status != release.StatusReady {
		t.Fatalf("internal app: %s %+v", d.Status, d.Error)
	}
	prod := newApp("Produccion", "production")
	if d := publish(prod, "b"); d.Status != release.StatusFailed || d.Error == nil || d.Error.Code != release.CodePolicyDenied {
		t.Fatalf("production app with criticals: %s %+v", d.Status, d.Error)
	}

	// the inventory: scan of what is online, and the refused publication
	var rows []inventory.Row
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		res := checked(t, app, http.MethodGet, "/api/v1/inventory", itToken, nil)
		httpxtest.Want(t, res, http.StatusOK)
		rows = httpxtest.JSON[inventory.Page](t, res).Items
		if len(rows) == 2 && rows[0].Scan.State == inventory.ScanVulnerable {
			break
		}
	}
	if len(rows) != 2 || rows[0].App.Name != "Interna" || rows[1].App.Name != "Produccion" {
		t.Fatalf("rows %+v", rows)
	}
	if s := rows[0].Scan; s.State != inventory.ScanVulnerable || s.Critical != 2 || s.High != 5 || s.ScannedAt == nil || !rows[0].NeedsAttention {
		t.Fatalf("scan of the online image %+v", rows[0])
	}
	if rows[1].Status != inventory.StatusFailed || rows[1].Online != nil || rows[1].App.Tier != "production" || !rows[1].NeedsAttention {
		t.Fatalf("refused production app %+v", rows[1])
	}

	// TI's list: vulnerable (critical), failed; the builder's apps are theirs too
	a := attention(itToken)
	if !has(a, inventory.KindAppVulnerable, inventory.ScopeIT, internal.id) || !has(a, inventory.KindDeploymentFailed, inventory.ScopeIT, prod.id) {
		t.Fatalf("TI's list %+v", a.Items)
	}
	if a.Items[0].Severity != inventory.SeverityCritical || a.Items[0].Scope != inventory.ScopeIT {
		t.Fatalf("the list is not ordered by what is worst: %+v", a.Items[0])
	}
	for _, i := range a.Items {
		if i.Sentence == "" || i.Link == "" {
			t.Fatalf("an item without sentence or link: %+v", i)
		}
	}
	b := attention(builder)
	if has(b, inventory.KindAppVulnerable, inventory.ScopeIT, "") || !has(b, inventory.KindDeploymentFailed, inventory.ScopeMine, prod.id) {
		t.Fatalf("the builder's list %+v", b.Items)
	}

	// a held request: TI decides it; the requester waits, then is told
	res := checked(t, app, http.MethodPut, internal.base+"/access", builder, map[string]any{"mode": "public"})
	httpxtest.Want(t, res, http.StatusAccepted)
	req := httpxtest.JSON[pendingBody](t, res).RequestID
	if a := attention(itToken); !has(a, inventory.KindApprovalPending, inventory.ScopeIT, internal.id) {
		t.Fatalf("a pending request is not in TI's list: %+v", a.Items)
	}
	if b := attention(builder); !has(b, inventory.KindRequestPending, inventory.ScopeMine, internal.id) || has(b, inventory.KindApprovalPending, inventory.ScopeIT, "") {
		t.Fatalf("the requester's list %+v", b.Items)
	}
	httpxtest.Want(t, checked(t, app, http.MethodPost, "/api/v1/approvals/"+req+"/reject", itToken, map[string]any{"note": "no por ahora"}), http.StatusOK)
	a = attention(itToken)
	b = attention(builder)
	if has(a, inventory.KindApprovalPending, inventory.ScopeIT, "") || has(b, inventory.KindRequestPending, inventory.ScopeMine, "") ||
		!has(b, inventory.KindRequestRejected, inventory.ScopeMine, internal.id) {
		t.Fatalf("after the rejection: TI %+v, requester %+v", a.Items, b.Items)
	}
}
