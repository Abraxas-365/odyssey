package bootstrap_test

// Contract test: api/openapi.yaml is the HTTP contract. It fails when
//
//  1. a mounted route is not documented, or a documented operation is not
//     mounted;
//  2. an operation's x-permissions differ from what the route enforces
//     (probed: without one of them → 403, with exactly them → not 403;
//     without a token → 401; public operations never ask for one);
//  3. a real response (see checked) does not validate against the schemas.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/api"
	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/config"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"
	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	vconfig "github.com/pb33f/libopenapi-validator/config"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"go.yaml.in/yaml/v3"
)

type operation struct {
	method, path string // path in Fiber syntax (/projects/:id)
	permissions  []string
	// anyUser: `x-permissions: []` written out on purpose: any authenticated
	// token may call it and the service decides per record (approvals: your
	// own requests, or the ones you can decide). A missing key stays an error.
	anyUser bool
	public  bool
	// mcp: secured by the agents' OAuth resource (scheme "mcp"), whose
	// permissions are per tool (mcp_tools_test), not per route.
	mcp bool
}

var spec struct {
	once      sync.Once
	model     *v3.Document
	validator validator.Validator
	err       error
}

func loadSpec(t *testing.T) (*v3.Document, validator.Validator) {
	t.Helper()
	spec.once.Do(func() {
		doc, err := libopenapi.NewDocument(api.OpenAPI)
		if err != nil {
			spec.err = err
			return
		}
		m, err := doc.BuildV3Model()
		if err != nil {
			spec.err = err
			return
		}
		spec.model = &m.Model
		v, errs := validator.NewValidator(doc, vconfig.WithFormatAssertions())
		if len(errs) > 0 {
			spec.err = errs[0]
			return
		}
		if ok, verrs := v.ValidateDocument(); !ok {
			for _, e := range verrs {
				spec.err = e
			}
			return
		}
		spec.validator = v
	})
	if spec.err != nil {
		t.Fatalf("api/openapi.yaml: %v", spec.err)
	}
	return spec.model, spec.validator
}

func operations(t *testing.T) []operation {
	model, _ := loadSpec(t)
	globalSecured := len(model.Security) > 0
	var out []operation
	for path, item := range model.Paths.PathItems.FromOldest() {
		fiberPath := path
		for strings.Contains(fiberPath, "{") {
			start, end := strings.Index(fiberPath, "{"), strings.Index(fiberPath, "}")
			fiberPath = fiberPath[:start] + ":" + fiberPath[start+1:end] + fiberPath[end+1:]
		}
		for method, op := range item.GetOperations().FromOldest() {
			o := operation{method: strings.ToUpper(method), path: fiberPath}
			o.public = op.Security != nil && len(op.Security) == 0 || !globalSecured
			for _, req := range op.Security {
				if _, ok := req.Requirements.Get("mcp"); ok {
					o.mcp = true
				}
			}
			if node, ok := op.Extensions.Get("x-permissions"); ok {
				if err := node.Decode(&o.permissions); err != nil {
					t.Fatalf("%s %s: x-permissions: %v", method, path, err)
				}
				o.anyUser = len(o.permissions) == 0
			}
			out = append(out, o)
		}
	}
	return out
}

// routeApp is the real server over an unreachable database: enough to list
// the routes and probe authorization (which runs before any handler).
func routeApp(t *testing.T) *fiber.App {
	t.Helper()
	db, err := sqlx.Open("postgres", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p, err := bootstrap.Assemble(context.Background(), db, bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return bootstrap.NewServer(p, config.Config{}, httpxtest.Auth(), testMCPAuth(t))
}

// testMCPAuth is a real MCP authenticator whose IAMKit is unreachable: it
// mounts /mcp and its metadata, and refuses every token (401 without one).
func testMCPAuth(t *testing.T) *httpx.MCPAuth {
	t.Helper()
	auth, err := httpx.NewMCPAuth(config.IAM{URL: "http://127.0.0.1:1", Issuer: "http://127.0.0.1:1", Audience: "http://localhost:8095/mcp",
		Environment: "env-1", Application: "app-1", Resource: "res-1"},
		config.MCP{IntrospectionClientID: "rs", IntrospectionClientSecret: "secret"}, permission.All)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestRoutesMatchTheOpenAPISpec(t *testing.T) {
	documented := map[string]bool{}
	for _, o := range operations(t) {
		documented[o.method+" "+o.path] = true
	}
	mounted := map[string]bool{}
	for _, r := range routeApp(t).GetRoutes(true) {
		if r.Method == fiber.MethodHead { // Fiber adds HEAD to every GET
			continue
		}
		path := r.Path
		if len(path) > 1 {
			path = strings.TrimRight(path, "/")
		}
		mounted[r.Method+" "+path] = true
	}
	var missing, phantom []string
	for k := range mounted {
		if !documented[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !mounted[k] {
			phantom = append(phantom, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(phantom)
	for _, k := range missing {
		t.Errorf("route %s is mounted but not documented in api/openapi.yaml", k)
	}
	for _, k := range phantom {
		t.Errorf("api/openapi.yaml documents %s but no route serves it", k)
	}
}

// TestSpecHasNoStrayKeys: a comma inside an unquoted flow-mapping value
// ({ description: a, b }) ends the scalar and leaves "b" as a key with no
// value. Every such null is a typo (the spec never uses null values).
func TestSpecHasNoStrayKeys(t *testing.T) {
	var doc any
	if err := yaml.Unmarshal(api.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(v any, at string)
	walk = func(v any, at string) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if x == nil {
					t.Errorf("api/openapi.yaml: %s/%q has no value (quote the description before it)", at, k)
				}
				walk(x, at+"/"+k)
			}
		case []any:
			for _, x := range v {
				walk(x, at)
			}
		}
	}
	walk(doc, "")
}

func TestOperationPermissionsMatchTheRoutes(t *testing.T) {
	app := routeApp(t)
	org := identity.NewOrganizationID()
	probe := func(o operation, token string) int {
		path := o.path
		for strings.Contains(path, ":") {
			start := strings.Index(path, ":")
			end := strings.IndexByte(path[start:], '/')
			if end < 0 {
				end = len(path) - start
			}
			path = path[:start] + identity.NewOrganizationID().String() + path[start+end:]
		}
		req := httptest.NewRequest(o.method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := app.Test(req, 5000)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, o := range operations(t) {
		name := o.method + " " + o.path
		for _, p := range o.permissions {
			if !slices.Contains(permission.All, p) {
				t.Errorf("%s: x-permissions names %q, which is not in iam/permissions.json", name, p)
			}
		}
		if o.public {
			if len(o.permissions) > 0 {
				t.Errorf("%s: a public operation cannot require permissions", name)
			}
			if s := probe(o, ""); s == http.StatusUnauthorized || s == http.StatusForbidden {
				t.Errorf("%s is public in the spec but answers %d without a token", name, s)
			}
			continue
		}
		if o.mcp {
			if len(o.permissions) > 0 {
				t.Errorf("%s: MCP permissions are per tool, not x-permissions", name)
			}
			if s := probe(o, ""); s != http.StatusUnauthorized {
				t.Errorf("%s without a token: %d, want 401", name, s)
			}
			// A console token is not for this resource.
			if s := probe(o, httpxtest.Token(org, permission.All...)); s != http.StatusUnauthorized {
				t.Errorf("%s with a console token: %d, want 401", name, s)
			}
			continue
		}
		if len(o.permissions) == 0 && !o.anyUser {
			t.Errorf("%s: secured operation without x-permissions", name)
			continue
		}
		if s := probe(o, ""); s != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d, want 401", name, s)
		}
		if o.anyUser {
			// no permission needed: a token with none still gets in (what it
			// sees or may do is the service's call, per record).
			if s := probe(o, httpxtest.Token(org)); s == http.StatusForbidden || s == http.StatusUnauthorized {
				t.Errorf("%s with a token holding no permission: %d (the spec says any user)", name, s)
			}
			continue
		}
		if s := probe(o, httpxtest.Token(org, o.permissions...)); s == http.StatusForbidden || s == http.StatusUnauthorized {
			t.Errorf("%s with exactly %v: %d (the route requires more than the spec says)", name, o.permissions, s)
		}
		for _, p := range o.permissions {
			others := slices.DeleteFunc(slices.Clone(permission.All), func(x string) bool { return x == p })
			if s := probe(o, httpxtest.Token(org, others...)); s != http.StatusForbidden {
				t.Errorf("%s without %s: %d, want 403 (the route does not enforce it)", name, p, s)
			}
		}
	}
}

// checked sends a request like httpxtest.Do and fails the test unless the
// response validates against the documented operation.
func checked(t *testing.T, app *fiber.App, method, path, token string, body any) httpxtest.Response {
	t.Helper()
	res := httpxtest.Do(t, app, method, path, token, body)
	validate(t, method, path, res)
	return res
}

// validate fails the test unless res matches the documented operation.
func validate(t *testing.T, method, path string, res httpxtest.Response) {
	t.Helper()
	_, v := loadSpec(t)
	req := httptest.NewRequest(method, path, nil)
	httpRes := &http.Response{
		StatusCode: res.Status, Header: res.Header, Request: req,
		Body: io.NopCloser(bytes.NewReader(res.Body)), ContentLength: int64(len(res.Body)),
	}
	if ok, errs := v.ValidateHttpResponse(req, httpRes); !ok {
		for _, e := range errs {
			msg := e.Error()
			for _, s := range e.SchemaValidationErrors {
				msg += "\n    " + s.FieldPath + ": " + s.Reason
			}
			t.Errorf("%s %s → %d does not match api/openapi.yaml: %s\n  body: %s", method, path, res.Status, msg, res.Body)
		}
	}
}
