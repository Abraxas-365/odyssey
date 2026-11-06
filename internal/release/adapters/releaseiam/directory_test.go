package releaseiam_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releaseiam"
)

// newUser is a user id as IAMKit would make it.
func newUser() identity.UserID { return identity.MustParseUserID(uuid.NewString()) }

// fakeIAM is IAMKit's machine-token and member-list endpoints.
func fakeIAM(t *testing.T, org identity.OrganizationID, members []map[string]any) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var tokens, lists atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /identity/v1/machine-token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ik_svc_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tokens.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "machine", "token_type": "Bearer", "expires_in": 900})
	})
	mux.HandleFunc("GET /api/v1/environments/env-1/organizations/{org}/members", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer machine" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		lists.Add(1)
		items := []map[string]any{}
		if r.PathValue("org") == org.String() {
			items = members
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "page": map[string]any{"total": len(items)}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &tokens, &lists
}

func TestDirectoryNamesMembers(t *testing.T) {
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	ana, gone, quiet := newUser(), newUser(), newUser()
	srv, tokens, lists := fakeIAM(t, org, []map[string]any{
		{"user_id": ana.String(), "user_name": "Ana Torres", "user_email": "ana@demo.local", "active": true},
		{"user_id": gone.String(), "user_name": "Ex Empleado", "active": false},
		{"user_id": quiet.String(), "user_name": "", "active": true},
	})
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	dir := releaseiam.New(srv.URL, "env-1", "ik_svc_secret", releaseiam.Options{Now: func() time.Time { return now }})
	ctx := context.Background()

	for _, c := range []struct {
		org  identity.OrganizationID
		user identity.UserID
		want string
	}{
		{org, ana, "Ana Torres"},
		{org, gone, ""},  // no longer a member
		{org, quiet, ""}, // never an email in its place
		{org, newUser(), ""},
		{other, ana, ""}, // another organization's directory does not know her
	} {
		got, err := dir.Name(ctx, c.org, c.user)
		if err != nil || got != c.want {
			t.Fatalf("Name(%s) = %q, %v; want %q", c.user, got, err, c.want)
		}
	}
	// one token, one list per organization within the TTL
	if tokens.Load() != 1 || lists.Load() != 2 {
		t.Fatalf("tokens %d lists %d", tokens.Load(), lists.Load())
	}
	now = now.Add(2 * time.Minute)
	if _, err := dir.Name(ctx, org, ana); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 1 || lists.Load() != 3 {
		t.Fatalf("after the TTL: tokens %d lists %d", tokens.Load(), lists.Load())
	}
	// the token is renewed before it expires
	now = now.Add(15 * time.Minute)
	if _, err := dir.Name(ctx, org, ana); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatalf("tokens %d", tokens.Load())
	}
}

func TestDirectoryFailsWhenIAMKitRefuses(t *testing.T) {
	org := identity.NewOrganizationID()
	srv, _, _ := fakeIAM(t, org, nil)
	dir := releaseiam.New(srv.URL, "env-1", "ik_svc_wrong", releaseiam.Options{})
	if _, err := dir.Name(context.Background(), org, newUser()); err == nil {
		t.Fatal("a refused service account named someone")
	}
}
