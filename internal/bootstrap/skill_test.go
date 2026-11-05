package bootstrap_test

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/skills"
)

// snakeName finds `snake_case` identifiers in backticks: anything that looks
// like a tool name.
var snakeName = regexp.MustCompile("`([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`")

// Every snake_case name the skill puts in backticks is a real MCP tool or a
// known parameter, and every tool is taught: the skill cannot drift from the
// MCP server it describes.
func TestSkillNamesRealTools(t *testing.T) {
	var real []string
	for _, tool := range bootstrap.MCPTools(mcpPlatform(t), config.Config{}) {
		real = append(real, tool.Name)
	}
	// parameters and results the skill may name
	params := []string{"deployment_id", "wait_seconds", "health_path", "next_step", "node_modules", "console_url",
		// get_app's needs_attention and the closed list of uses.attention kinds
		"needs_attention", "secret_missing", "secrets_unavailable", "resource_not_ready", "resource_failed", "ai_error", "ai_pending", "config_pending", "config_failed", "egress_not_applied"}
	mentioned := map[string]bool{}
	err := fs.WalkDir(skills.FS, "odicea", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := skills.FS.ReadFile(path)
		for _, name := range real {
			if strings.Contains(string(raw), "`"+name+"`") {
				mentioned[name] = true
			}
		}
		for _, m := range snakeName.FindAllStringSubmatch(string(raw), -1) {
			if !slices.Contains(real, m[1]) && !slices.Contains(params, m[1]) {
				t.Errorf("%s names `%s`, which is neither an MCP tool nor a known parameter", path, m[1])
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range real {
		if !mentioned[name] {
			t.Errorf("tool %s is not mentioned in the skill", name)
		}
	}
}

func TestAssistantAndSkillRoutes(t *testing.T) {
	platform := mcpPlatform(t)
	org := identity.NewOrganizationID()
	token := httpxtest.Token(org, permission.ProjectsRead)
	bundle := skills.Default()

	cfg := config.Config{}
	cfg.IAM.Audience = "https://odicea.example.com/mcp"
	cfg.MCP = config.MCP{IntrospectionClientID: "rs", IntrospectionClientSecret: "s", Clients: []string{"codex", "claude_code"}}
	app := bootstrap.NewServer(platform, cfg, httpxtest.Auth(), nil)

	res := checked(t, app, http.MethodGet, "/api/v1/assistant", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	var got struct {
		Available bool   `json:"available"`
		MCPURL    string `json:"mcp_url"`
		Skill     struct{ URL, Version, Name string }
		Clients   []struct {
			Key          string
			Install      string
			SkillInstall string `json:"skill_install"`
		}
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.MCPURL != cfg.IAM.Audience || got.Skill.URL != "https://odicea.example.com/skills/odicea.tar.gz" ||
		got.Skill.Version != bundle.Version || got.Skill.Name != "odicea" || len(got.Clients) != 2 || got.Clients[0].Key != "codex" ||
		!strings.Contains(got.Clients[1].Install, "claude mcp add") || !strings.Contains(got.Clients[1].SkillInstall, got.Skill.URL) {
		t.Fatalf("assistant: %s", res.Body)
	}

	// MCP off: nothing is offered, and the answer still holds the shape
	off := bootstrap.NewServer(platform, config.Config{}, httpxtest.Auth(), nil)
	res = checked(t, off, http.MethodGet, "/api/v1/assistant", token, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if !strings.Contains(string(res.Body), `"available":false`) || !strings.Contains(string(res.Body), `"clients":[]`) {
		t.Fatalf("assistant without MCP: %s", res.Body)
	}

	// the skill: public, with its version as ETag, 304 on a match
	get := func(inm string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/skills/odicea.tar.gz", nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		r, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Body.Close() })
		return r
	}
	r := get("")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "application/gzip" || r.Header.Get("ETag") != `"`+bundle.Version+`"` ||
		r.Header.Get("Cache-Control") != "public, max-age=300" || string(body) != string(bundle.Archive) {
		t.Fatalf("download: %d %v (%d bytes)", r.StatusCode, r.Header, len(body))
	}
	q := `"` + bundle.Version + `"`
	for inm, want := range map[string]int{
		q:                             http.StatusNotModified,
		"W/" + q:                      http.StatusNotModified, // weak validator
		`"other", ` + q + `, "third"`: http.StatusNotModified, // a list
		`W/"other",W/` + q:            http.StatusNotModified,
		"*":                           http.StatusNotModified,
		`"other"`:                     http.StatusOK,
		`"other", W/"another"`:        http.StatusOK,
		strings.Trim(q, `"`):          http.StatusOK, // unquoted is not a validator
	} {
		if r := get(inm); r.StatusCode != want {
			t.Errorf("If-None-Match %s: %d, want %d", inm, r.StatusCode, want)
		}
	}
}
