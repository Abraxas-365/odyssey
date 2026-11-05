package approvalhttp_test

import (
	"net/http"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/approval"
	"github.com/Abraxas-365/odyssey/internal/approval/adapters/approvalhttp"
	"github.com/Abraxas-365/odyssey/internal/approval/approvalsvc"
	"github.com/Abraxas-365/odyssey/internal/approval/approvaltest"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

const itUser = "00000000-0000-4000-8000-0000000000ee"

func TestApprovalRoutes(t *testing.T) {
	svc := approvalsvc.New(approvaltest.NewRepo(), txn.Passthrough{}, &eventtest.Recorder{})
	app := httpxtest.App()
	app.Use(httpx.Provenance)
	approvalhttp.New(svc, svc).Register(app.Group("/api/v1"))
	org := identity.NewOrganizationID()
	asker := httpxtest.Token(org, permission.ProjectsWrite)
	it := httpxtest.TokenAs(itUser, org, permission.ApprovalsDecide)
	itSelf := httpxtest.Token(org, permission.ApprovalsDecide) // same subject as the asker
	other := httpxtest.Token(identity.NewOrganizationID(), permission.ApprovalsDecide)
	do := func(method, path, token string, body any) httpxtest.Response {
		return httpxtest.Do(t, app, method, path, token, body)
	}
	open := func(payload string) approval.Request {
		r, err := svc.Open(t.Context(), org, approval.Open{Action: "set_access", Project: project.NewID(), Environment: project.NewEnvironmentID(),
			Summary: "Hacer pública «app»", Payload: map[string]string{"mode": payload},
			Requester: approval.Requester{User: identity.MustParseUserID(httpxtest.User)}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := open("public")
	path := "/api/v1/approvals/" + r.ID.String()

	// the asker sees their own; IT sees what it could decide; the asker cannot decide
	res := do(http.MethodGet, "/api/v1/approvals?status=pending", asker, nil)
	httpxtest.Want(t, res, http.StatusOK)
	list := httpxtest.JSON[struct {
		Items []approval.View
	}](t, res)
	if len(list.Items) != 1 || !list.Items[0].Mine || list.Items[0].CanDecide {
		t.Fatalf("asker list %+v", list.Items)
	}
	res = do(http.MethodGet, "/api/v1/approvals?decidable=true", it, nil)
	if got := httpxtest.JSON[struct{ Items []approval.View }](t, res); len(got.Items) != 1 || !got.Items[0].CanDecide {
		t.Fatalf("decidable %+v", got.Items)
	}
	if got := httpxtest.JSON[struct{ Items []approval.View }](t, do(http.MethodGet, "/api/v1/approvals?decidable=true", itSelf, nil)); len(got.Items) != 0 {
		t.Fatalf("a requester holding the permission decides their own: %+v", got.Items)
	}
	if got := httpxtest.JSON[struct{ Items []approval.View }](t, do(http.MethodGet, "/api/v1/approvals", other, nil)); len(got.Items) != 0 {
		t.Fatalf("another organization sees %+v", got.Items)
	}
	for _, bad := range []string{"?status=nope", "?mine=maybe", "?decidable=2", "?mine=true&decidable=true"} {
		httpxtest.Want(t, do(http.MethodGet, "/api/v1/approvals"+bad, it, nil), http.StatusBadRequest)
	}
	httpxtest.Want(t, do(http.MethodGet, path, other, nil), http.StatusNotFound)
	httpxtest.Want(t, do(http.MethodGet, "/api/v1/approvals/not-a-uuid", it, nil), http.StatusBadRequest)
	if v := httpxtest.JSON[approval.View](t, do(http.MethodGet, path, it, nil)); v.ID != r.ID || !v.CanDecide {
		t.Fatalf("get %+v", v)
	}

	// deciding: not your own (403 APPROVAL_SELF), not without the permission, a note over the limit is 400
	res = do(http.MethodPost, path+"/approve", itSelf, nil)
	httpxtest.Want(t, res, http.StatusForbidden)
	if httpxtest.ErrorCode(res) != approval.CodeSelf {
		t.Fatalf("code %q", httpxtest.ErrorCode(res))
	}
	httpxtest.Want(t, do(http.MethodPost, path+"/approve", other, nil), http.StatusNotFound)
	httpxtest.Want(t, do(http.MethodPost, path+"/approve", it, map[string]string{"note": string(make([]rune, 501))}), http.StatusBadRequest)
	httpxtest.Want(t, do(http.MethodPost, path+"/approve", it, map[string]any{"nope": 1}), http.StatusBadRequest)
	res = do(http.MethodPost, path+"/approve", it, map[string]string{"note": "va"})
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[approval.Request](t, res); got.Status != approval.StatusApproved || got.DecisionNote != "va" {
		t.Fatalf("approved %+v", got)
	}
	res = do(http.MethodPost, path+"/reject", it, nil)
	httpxtest.Want(t, res, http.StatusConflict)
	if httpxtest.ErrorCode(res) != approval.CodeDecided {
		t.Fatalf("code %q", httpxtest.ErrorCode(res))
	}

	// reject with no body; cancel is the asker's
	r2 := open("team")
	p2 := "/api/v1/approvals/" + r2.ID.String()
	httpxtest.Want(t, do(http.MethodPost, p2+"/cancel", it, nil), http.StatusForbidden)
	res = do(http.MethodPost, p2+"/cancel", asker, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if got := httpxtest.JSON[approval.Request](t, res); got.Status != approval.StatusCancelled {
		t.Fatalf("cancelled %+v", got)
	}
	r3 := open("private")
	httpxtest.Want(t, do(http.MethodPost, "/api/v1/approvals/"+r3.ID.String()+"/reject", it, nil), http.StatusOK)
}
