package catalogiam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/catalog/adapters/catalogiam"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// fakeIAM is IAMKit's group endpoints for org: Datos (by hand) and Ventas
// (synced from the directory); ana is in both.
func fakeIAM(t *testing.T, org identity.OrganizationID, ana identity.UserID) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	conn := "conn_1"
	groups := []map[string]any{
		{"id": "grp_ventas", "name": "ventas", "member_count": 3, "connection_id": conn},
		{"id": "grp_datos", "name": "Datos", "member_count": 2, "connection_id": nil},
	}
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer machine" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		reads.Add(1)
		return true
	}
	list := func(w http.ResponseWriter, items []map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "page": map[string]any{"total": len(items)}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/environments/env-1/organizations/{org}/groups", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.PathValue("org") != org.String() {
			list(w, []map[string]any{})
			return
		}
		list(w, groups)
	})
	mux.HandleFunc("GET /api/v1/environments/env-1/organizations/{org}/members/{user}/groups", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.PathValue("org") != org.String() || r.PathValue("user") != ana.String() {
			list(w, []map[string]any{})
			return
		}
		list(w, groups)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &reads
}

func TestDirectoryReadsTeams(t *testing.T) {
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	ana, bruno := identity.MustParseUserID("11111111-1111-4111-8111-111111111111"), identity.MustParseUserID("22222222-2222-4222-8222-222222222222")
	srv, reads := fakeIAM(t, org, ana)
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	token := "machine"
	forgot := 0
	dir := catalogiam.New(srv.URL, "env-1", catalogiam.Token{
		Get:    func(context.Context) (string, error) { return token, nil },
		Forget: func() { forgot++ },
	}, catalogiam.Options{Now: func() time.Time { return now }})
	ctx := context.Background()

	teams, err := dir.Teams(ctx, org)
	want := []catalog.Team{{ID: "grp_datos", Name: "Datos", Members: 2}, {ID: "grp_ventas", Name: "ventas", Members: 3, Directory: true}}
	if err != nil || !slices.Equal(teams, want) {
		t.Fatalf("teams (sorted by name, directory flag): %+v %v", teams, err)
	}
	if mine, err := dir.TeamsOf(ctx, org, ana); err != nil || !slices.Equal(mine, []string{"grp_datos", "grp_ventas"}) {
		t.Fatalf("ana's teams: %v %v", mine, err)
	}
	if mine, err := dir.TeamsOf(ctx, org, bruno); err != nil || len(mine) != 0 {
		t.Fatalf("bruno's teams: %v %v", mine, err)
	}
	// another organization's groups are its own
	if theirs, err := dir.Teams(ctx, other); err != nil || len(theirs) != 0 {
		t.Fatalf("other organization: %+v %v", theirs, err)
	}

	// a member's teams are not asked again within the TTL; after it, they are.
	// The organization's list is always fresh (a team just made is choosable).
	before := reads.Load()
	_, _ = dir.TeamsOf(ctx, org, ana)
	if reads.Load() != before {
		t.Fatalf("cached read went to IAMKit: %d → %d", before, reads.Load())
	}
	_, _ = dir.Teams(ctx, org)
	if reads.Load() != before+1 {
		t.Fatalf("the organization's teams were cached: %d → %d", before, reads.Load())
	}
	before = reads.Load()
	now = now.Add(2 * time.Minute)
	if _, err := dir.TeamsOf(ctx, org, ana); err != nil || reads.Load() != before+1 {
		t.Fatalf("expired read: %d %v", reads.Load(), err)
	}

	// a refused token is dropped (asked for again next time) and the error is external
	token = "revoked"
	now = now.Add(2 * time.Minute)
	if _, err := dir.TeamsOf(ctx, org, ana); !errx.IsType(err, errx.TypeExternal) || forgot != 1 {
		t.Fatalf("refused: %v (forgot %d)", err, forgot)
	}
}

func TestDirectoryWithoutAToken(t *testing.T) {
	dir := catalogiam.New("http://127.0.0.1:1", "env-1", catalogiam.Token{
		Get: func(context.Context) (string, error) { return "", errors.New("no secret") },
	}, catalogiam.Options{})
	if _, err := dir.Teams(context.Background(), identity.NewOrganizationID()); err == nil {
		t.Fatal("teams without a token")
	}
}
