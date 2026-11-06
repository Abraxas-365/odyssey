package bootstrap_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/event/adapters/outboxpg"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
)

// people is a directory that knows one name per user.
type people struct {
	mu    sync.Mutex
	names map[identity.UserID]string
}

func (p *people) Name(_ context.Context, _ identity.OrganizationID, user identity.UserID) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.names[user], nil
}

func (p *people) rename(user identity.UserID, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.names[user] = name
}

// TestVersionsRememberWhoAskedByName: the request answers at once with no
// name (the directory is never on the request path); the worker then records
// the directory's name from deployment.started, and it is a snapshot: a later
// rename leaves earlier versions as they were.
func TestVersionsRememberWhoAskedByName(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	user := identity.MustParseUserID(httpxtest.User)
	dir := &people{names: map[identity.UserID]string{user: "Ana Torres"}}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{
		Workflow: workflowpg.Options{PollInterval: 20 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }},
		People:   dir,
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
	token := httpxtest.Token(org, permission.ProjectsRead, permission.ProjectsWrite, permission.DeploymentsRead, permission.DeploymentsCreate)
	res := checked(t, app, http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Visitas"})
	httpxtest.Want(t, res, http.StatusCreated)
	p := httpxtest.JSON[project.Project](t, res)
	envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, p.ID, project.EnvironmentFilter{})
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments %v %v", envs, err)
	}
	base := "/api/v1/projects/" + p.ID.String() + "/environments/" + envs[0].ID.String()

	deploy := func(img string) release.Deployment {
		t.Helper()
		res := checked(t, app, http.MethodPost, base+"/deployments", token, map[string]any{"image": img, "port": 8080, "health_path": "/health"})
		httpxtest.Want(t, res, http.StatusAccepted)
		d := httpxtest.JSON[release.Deployment](t, res)
		if d.RequestedByName != "" || d.RequestedVia != release.ViaConsole {
			t.Fatalf("the request waited on the directory, or misattributed: %q %q", d.RequestedByName, d.RequestedVia)
		}
		return d
	}
	named := func(id release.DeploymentID, want string) {
		t.Helper()
		var got string
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			res := checked(t, app, http.MethodGet, base+"/deployments/"+id.String(), token, nil)
			httpxtest.Want(t, res, http.StatusOK)
			if got = httpxtest.JSON[release.Deployment](t, res).RequestedByName; got == want {
				return
			}
		}
		t.Fatalf("deployment %s named %q, want %q", id, got, want)
	}

	first := deploy(image("1"))
	named(first.ID, "Ana Torres")
	dir.rename(user, "Ana Torres Vega")
	second := deploy(image("2"))
	named(second.ID, "Ana Torres Vega")
	named(first.ID, "Ana Torres") // the snapshot stays
}
