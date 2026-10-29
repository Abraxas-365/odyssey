package buildhttp_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildhttp"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// queries holds one build and records the log query it was asked.
type queries struct {
	b     build.Build
	asked build.LogQuery
}

func (q *queries) Find(_ context.Context, org identity.OrganizationID, id build.ID) (build.Build, error) {
	if org != q.b.Organization || id != q.b.ID {
		return build.Build{}, errx.NotFound("build not found")
	}
	return q.b, nil
}

func (q *queries) FindSource(context.Context, identity.OrganizationID, build.SourceID) (build.Source, error) {
	return build.Source{}, errx.NotFound("source not found")
}

func (q *queries) Logs(_ context.Context, _ identity.OrganizationID, _ build.ID, lq build.LogQuery) (build.LogPage, error) {
	q.asked = lq
	return build.LogPage{Lines: []build.LogLine{{Seq: lq.After + 1, Stream: "build", Text: "ok"}}, Next: lq.After + 1}, nil
}

func TestBuildRoutes(t *testing.T) {
	org := identity.NewOrganizationID()
	q := &queries{b: build.Build{ID: build.NewID(), Organization: org, Project: project.NewID(), Status: build.StatusRunning}}
	app := httpxtest.App()
	buildhttp.New(q).Register(app.Group("/api/v1"))
	reader := httpxtest.Token(org, permission.DeploymentsRead)
	path := "/api/v1/projects/" + q.b.Project.String() + "/builds/" + q.b.ID.String()

	res := httpxtest.Do(t, app, http.MethodGet, path, reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[build.Build](t, res); got.ID != q.b.ID || got.Status != build.StatusRunning {
		t.Fatalf("build %+v", got)
	}

	res = httpxtest.Do(t, app, http.MethodGet, path+"/logs?after=41&limit=5000", reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if page := httpxtest.JSON[build.LogPage](t, res); len(page.Lines) != 1 || page.Next != 42 {
		t.Fatalf("page %+v", page)
	}
	if q.asked.After != 41 || q.asked.Limit != build.MaxLogPage {
		t.Fatalf("asked %+v", q.asked)
	}

	// Another project's path, another organization, no permission, bad input.
	other := "/api/v1/projects/" + project.NewID().String() + "/builds/" + q.b.ID.String()
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, other, reader, nil), http.StatusNotFound)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, other+"/logs", reader, nil), http.StatusNotFound)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, path, httpxtest.Token(identity.NewOrganizationID(), permission.DeploymentsRead), nil), http.StatusNotFound)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, path, httpxtest.Token(org, permission.ProjectsRead), nil), http.StatusForbidden)
	httpxtest.Want(t, httpxtest.Do(t, app, http.MethodGet, "/api/v1/projects/"+q.b.Project.String()+"/builds/nope", reader, nil), http.StatusBadRequest)
	res = httpxtest.Do(t, app, http.MethodGet, path+"/logs?after=-1", reader, nil)
	httpxtest.Want(t, res, http.StatusBadRequest)
	if httpxtest.ErrorField(res) != "after" {
		t.Fatalf("field %q", httpxtest.ErrorField(res))
	}
}
