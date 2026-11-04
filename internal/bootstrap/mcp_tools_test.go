package bootstrap_test

// MCP contract (plan 1d §8 rule 5): the tools agents see are a contract like
// api/openapi.yaml.
//
//   - every tool's permissions are in iam/permissions.json;
//   - a tool whose risk is not read needs a permission that is not :read;
//   - names are unique snake_case (mcpx.NewRegistry);
//   - testdata/mcp/tools.golden.json is what `tools/list` answers for TI
//     (every tool: names, schemas, annotations) and which tools each role
//     sees. A change that breaks agents shows in the PR's diff. Regenerate
//     with ODYSSEY_UPDATE_GOLDEN=1 go test ./internal/bootstrap -run MCPToolsContract.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/mcpx"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/workflow/adapters/workflowpg"
	"github.com/jmoiron/sqlx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpGolden = "testdata/mcp/tools.golden.json"

func mcpPlatform(t *testing.T) *bootstrap.Platform {
	t.Helper()
	db, err := sqlx.Open("postgres", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p, err := bootstrap.Assemble(context.Background(), db, bootstrap.Options{Workflow: workflowpg.Options{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// listTools is tools/list over the real handler for a caller holding perms.
func listTools(t *testing.T, h http.Handler, perms []string) []*mcp.Tool {
	t.Helper()
	caller := mcpx.Principal{Organization: identity.NewOrganizationID(), User: identity.MustParseUserID("11111111-1111-4111-8111-111111111111"),
		Client: "https://claude.ai/oauth/claude-code-client-metadata", Permissions: perms}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+mcpx.VerifiedBearer)
		h.ServeHTTP(w, r.WithContext(mcpx.WithPrincipal(r.Context(), caller)))
	}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "contract", Version: "1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tools
}

func TestMCPToolsContract(t *testing.T) {
	p := mcpPlatform(t)
	cfg := config.Config{ConsoleURL: "http://localhost:5178"}
	tools := bootstrap.MCPTools(p, cfg)
	if _, err := mcpx.NewRegistry(tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		for _, perm := range tool.Permissions {
			if !slices.Contains(permission.All, perm) {
				t.Errorf("tool %s requires %q, which is not in iam/permissions.json", tool.Name, perm)
			}
		}
		// A tool that changes something is never open to a read-only role.
		if tool.Risk != mcpx.RiskRead && !slices.ContainsFunc(tool.Permissions, func(p string) bool { return !strings.HasSuffix(p, ":read") }) {
			t.Errorf("tool %s has risk %s but only read permissions %v: it needs the permission the REST route for the same change requires",
				tool.Name, tool.Risk, tool.Permissions)
		}
	}

	h := bootstrap.NewMCPHandler(p, cfg)
	golden := struct {
		Roles map[string][]string `json:"roles"`
		Tools []*mcp.Tool         `json:"tools"`
	}{Roles: map[string][]string{}, Tools: listTools(t, h, permission.All)}
	if len(golden.Tools) != len(tools) {
		t.Fatalf("every permission lists %d tools of %d", len(golden.Tools), len(tools))
	}
	for _, role := range permission.Roles {
		names := []string{}
		for _, tool := range listTools(t, h, role.Permissions) {
			names = append(names, tool.Name)
		}
		golden.Roles[role.Name] = names
	}
	if got := listTools(t, h, nil); len(got) != 0 {
		t.Errorf("a caller without permissions lists %d tools", len(got))
	}
	got, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("ODYSSEY_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata/mcp", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mcpGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(mcpGolden)
	if err != nil {
		t.Fatalf("%v (create it with ODYSSEY_UPDATE_GOLDEN=1)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("tools/list changed: agents see a different contract. Review and regenerate %s with ODYSSEY_UPDATE_GOLDEN=1.\n%s", mcpGolden, got)
	}
}
