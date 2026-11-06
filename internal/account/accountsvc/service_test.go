package accountsvc_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/account"
	"github.com/Abraxas-365/odyssey/internal/account/accountsvc"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

type fakeConns struct {
	byOrg        map[identity.OrganizationID][]account.Connected
	err          error
	disconnected []string
	tokens       []string
}

func (f *fakeConns) List(_ context.Context, org identity.OrganizationID, token string) ([]account.Connected, error) {
	f.tokens = append(f.tokens, token)
	return f.byOrg[org], f.err
}

func (f *fakeConns) Disconnect(_ context.Context, org identity.OrganizationID, token, id string) error {
	f.tokens = append(f.tokens, token)
	f.disconnected = append(f.disconnected, org.String()+"/"+id)
	return nil
}

var tools = map[string][]string{
	"list_apps":     {permission.ProjectsRead},
	"deploy_folder": {permission.DeploymentsCreate, permission.ProjectsRead},
	"rollback":      {permission.DeploymentsRollback, permission.ProjectsRead},
}

func rolePerms(t *testing.T, name string) []string {
	t.Helper()
	for _, r := range permission.Roles {
		if r.Name == name {
			return r.Permissions
		}
	}
	t.Fatalf("no role %s", name)
	return nil
}

func TestMe(t *testing.T) {
	ctx := context.Background()
	orgA, orgB := identity.NewOrganizationID(), identity.NewOrganizationID()
	user := identity.MustParseUserID("6f1c2a3b-0000-4000-8000-000000000001")
	older, newer := time.Now().Add(-time.Hour), time.Now().Add(-5*time.Minute)
	conns := &fakeConns{byOrg: map[identity.OrganizationID][]account.Connected{
		orgA: {
			{ID: "c1", ClientID: "https://chatgpt.com/oauth/codex/client.json", ConsentedAt: &older, LastUsedAt: &older},
			{ID: "c2", ClientID: "https://claude.ai/oauth/claude-code-client-metadata", ConsentedAt: &older, LastUsedAt: &newer},
			{ID: "c3", ClientID: "cursor-id", ConsentedAt: &older},
			// granted by the administrator: never consented nor used
			{ID: "c4", ClientID: "https://antigravity.google/oauth/client.json"},
			{ID: "c0", ClientID: "https://code.visualstudio.com/oauth/client.json", ConsentedAt: &newer},
		},
		orgB: {{ID: "b1", ClientID: "something", ConsentedAt: &older}},
	}}
	svc := accountsvc.New(conns, assistant.Registered{assistant.Cursor: "cursor-id"}, tools)

	lector := rolePerms(t, "Lector")
	me, err := svc.Me(ctx, orgA, account.Caller{User: user, Permissions: slices.Clone(lector), Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if me.User != user || me.Organization != orgA || me.Role != "Lector" || !slices.IsSorted(me.Permissions) || len(me.Permissions) != len(lector) {
		t.Fatalf("me %+v", me)
	}
	if me.Assistant == nil {
		t.Fatal("no assistant")
	}
	var keys, ids []string
	for _, c := range me.Assistant.Connected {
		keys = append(keys, c.Key)
		ids = append(ids, c.ID)
		if c.Name != assistant.Names[c.Key] {
			t.Errorf("name %+v", c)
		}
	}
	// used last first; then the never-used, connected last first; then the
	// ones the administrator granted
	if !slices.Equal(ids, []string{"c2", "c1", "c0", "c3", "c4"}) || keys[0] != assistant.ClaudeCode || keys[1] != assistant.Codex || keys[3] != assistant.Cursor {
		t.Fatalf("order %v %v", ids, keys)
	}
	// a reader's assistant reads, and is told what it cannot do
	if !slices.Equal(me.Assistant.Can, []string{assistant.Capabilities[0].Can}) || len(me.Assistant.Cannot) != 2 {
		t.Fatalf("abilities %+v", me.Assistant.Abilities)
	}
	if !slices.Equal(conns.tokens, []string{"tok"}) {
		t.Fatalf("IAMKit is asked with the caller's token: %v", conns.tokens)
	}

	// tenant isolation: the other organization's connections are its own
	me, err = svc.Me(ctx, orgB, account.Caller{User: user, Permissions: rolePerms(t, "Builder")})
	if err != nil || len(me.Assistant.Connected) != 1 || me.Assistant.Connected[0].ID != "b1" || me.Assistant.Connected[0].Key != assistant.Other || me.Role != "Builder" {
		t.Fatalf("org B: %+v %v", me, err)
	}
}

func TestMeWithoutIAMKitSaysNothing(t *testing.T) {
	ctx := context.Background()
	org := identity.NewOrganizationID()
	for name, svc := range map[string]*accountsvc.Service{
		"mcp off":        accountsvc.New(nil, nil, tools),
		"iamkit failing": accountsvc.New(&fakeConns{err: errors.New("down")}, nil, tools),
	} {
		me, err := svc.Me(ctx, org, account.Caller{User: identity.MustParseUserID("6f1c2a3b-0000-4000-8000-000000000001"), Permissions: []string{"custom"}})
		if err != nil || me.Assistant != nil || me.Role != "" || !slices.Equal(me.Permissions, []string{"custom"}) {
			t.Errorf("%s: %+v %v", name, me, err)
		}
	}
	me, _ := accountsvc.New(nil, nil, nil).Me(ctx, org, account.Caller{})
	if me.Permissions == nil {
		t.Error("permissions must be [] not null")
	}
}

func TestDisconnect(t *testing.T) {
	ctx := context.Background()
	orgA, orgB := identity.NewOrganizationID(), identity.NewOrganizationID()
	now := time.Now()
	conns := &fakeConns{byOrg: map[identity.OrganizationID][]account.Connected{
		orgA: {{ID: "c1", ClientID: "x", ConsentedAt: &now}},
		orgB: {{ID: "b1", ClientID: "y", ConsentedAt: &now}},
	}}
	svc := accountsvc.New(conns, nil, tools)
	who := account.Caller{User: identity.MustParseUserID("6f1c2a3b-0000-4000-8000-000000000001"), Token: "tok"}
	if err := svc.Disconnect(ctx, orgA, who, "c1"); err != nil {
		t.Fatal(err)
	}
	// another organization's connection, an unknown one, none: refused before IAMKit
	for _, id := range []string{"b1", "nope"} {
		if err := svc.Disconnect(ctx, orgA, who, id); !errx.IsType(err, errx.TypeNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	if err := svc.Disconnect(ctx, orgA, who, ""); err == nil {
		t.Error("empty id accepted")
	}
	if err := accountsvc.New(nil, nil, nil).Disconnect(ctx, orgA, who, "c1"); !errx.IsType(err, errx.TypeNotFound) {
		t.Errorf("mcp off: %v", err)
	}
	if !slices.Equal(conns.disconnected, []string{orgA.String() + "/c1"}) {
		t.Fatalf("disconnected %v", conns.disconnected)
	}
}

func TestRole(t *testing.T) {
	for _, r := range permission.Roles {
		perms := slices.Clone(r.Permissions)
		slices.Reverse(perms)
		if got := account.Role(perms); got != r.Name {
			t.Errorf("%s: got %q", r.Name, got)
		}
		if len(perms) > 1 {
			if got := account.Role(perms[1:]); got == r.Name {
				t.Errorf("%s minus one is still %s", r.Name, got)
			}
		}
	}
	if account.Role(nil) != "" {
		t.Error("no permissions has a role")
	}
}
