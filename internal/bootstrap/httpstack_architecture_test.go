package bootstrap_test

// One HTTP stack: every server (the API, the gateway, the agent's source
// server) and every test runs on net/http (httpx.Router over http.ServeMux).
// Fiber and fasthttp were removed (they forked the request model: handlers,
// middleware and the IAMKit SDK adapter came in two flavours); a second
// framework must not come back through any package, test or tool.

import (
	"sort"
	"strconv"
	"strings"
	"testing"
)

// otherHTTPStacks are the server frameworks the repository does not use.
var otherHTTPStacks = []string{
	"github.com/gofiber/", "github.com/valyala/fasthttp",
	"github.com/labstack/echo", "github.com/gin-gonic/gin",
}

func TestOneHTTPStack(t *testing.T) {
	report(t, httpStackViolations(repoSources(t, "cmd", "internal", "pkg", "agent")))
}

func httpStackViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, stack := range otherHTTPStacks {
				if p == strings.TrimSuffix(stack, "/") || strings.HasPrefix(p, stack) {
					out = append(out, f.fset.Position(imp.Pos()).String()+": "+f.rel+" imports "+p+
						" (the HTTP stack is net/http with httpx.Router)")
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestOneHTTPStackCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"internal/widget/adapters/widgethttp/ok.go":    `package widgethttp; import (_ "net/http"; _ "github.com/Abraxas-365/iamkit/sdk/authclient/httpauth")`,
		"internal/widget/adapters/widgethttp/fiber.go": `package widgethttp; import _ "github.com/gofiber/fiber/v2"`,
		"internal/httpx/adaptor.go":                    `package httpx; import _ "github.com/gofiber/fiber/v2/middleware/adaptor"`,
		"pkg/fast/f.go":                                `package fast; import _ "github.com/valyala/fasthttp"`,
		"cmd/tool/main_test.go":                        `package main; import _ "github.com/labstack/echo/v4"`,
		"agent/x/g.go":                                 `package x; import _ "github.com/gin-gonic/gin"`,
	})
	expectViolations(t, httpStackViolations(files),
		"internal/widget/adapters/widgethttp/fiber.go: imports github.com/gofiber/fiber/v2",
		"internal/httpx/adaptor.go: imports github.com/gofiber/fiber/v2/middleware/adaptor",
		"pkg/fast/f.go: imports github.com/valyala/fasthttp",
		"cmd/tool/main_test.go: imports github.com/labstack/echo/v4",
		"agent/x/g.go: imports github.com/gin-gonic/gin",
	)
}
