package detect_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/detect"
)

func names(p detect.Proposal) []string {
	var out []string
	for _, s := range p.Services {
		out = append(out, s.Name+":"+s.Kind+":"+s.Root+":"+s.Route)
	}
	return out
}

func TestServices(t *testing.T) {
	cases := map[string]struct {
		paths []string
		root  string
		want  []string
	}{
		"one app at the root": {
			paths: []string{"package.json", "src/index.js", "README.md"},
			want:  []string{"web:web::/"},
		},
		"a monorepo web + api": {
			paths: []string{"package.json", "apps/web/package.json", "apps/web/src/app.tsx", "apps/api/go.mod", "apps/api/main.go",
				"packages/ui/package.json", "node_modules/x/package.json"},
			want: []string{"web:web:apps/web:/", "api:web:apps/api:/api"},
		},
		"the site goes first, the rest under their name, a worker has no route": {
			paths: []string{"services/billing/Dockerfile", "services/frontend/package.json", "services/worker/go.mod"},
			want:  []string{"frontend:web:services/frontend:/", "billing:web:services/billing:/billing", "worker:worker:services/worker:"},
		},
		"no site name: the first web one at /": {
			paths: []string{"api/go.mod", "admin/package.json"},
			want:  []string{"admin:web:admin:/", "api:web:api:/api"},
		},
		"an app inside an app is part of it": {
			paths: []string{"web/package.json", "web/server/go.mod", "api/pyproject.toml"},
			want:  []string{"web:web:web:/", "api:web:api:/api"},
		},
		"client SDKs are libraries, not apps": {
			paths: []string{"Dockerfile", "go.mod", "frontend/package.json", "sdk/go.mod", "clients/js/package.json", "clients/python/pyproject.toml"},
			want:  []string{"frontend:web:frontend:/"},
		},
		"dependencies, outputs, hidden and library folders are not apps": {
			paths: []string{"go.mod", "vendor/a/go.mod", ".github/actions/x/package.json", "dist/package.json", "libs/auth/package.json",
				"internal/tool/go.mod", "docs/site/package.json", "examples/demo/package.json"},
			want: []string{"web:web::/"},
		},
		"too deep is not read": {
			paths: []string{"a/b/c/d/package.json", "go.mod"},
			want:  []string{"web:web::/"},
		},
		"names are made valid and unique": {
			paths: []string{"apps/My-Web_App/package.json", "services/my-web-app/go.mod", "2fast/Dockerfile"},
			want:  []string{"fast:web:2fast:/", "mywebapp:web:apps/My-Web_App:/mywebapp", "mywebapp2:web:services/my-web-app:/mywebapp2"},
		},
		"under the connection's folder only, relative to it": {
			paths: []string{"go.mod", "platform/web/package.json", "platform/api/go.mod", "other/x/package.json"},
			root:  "/platform/",
			want:  []string{"web:web:web:/", "api:web:api:/api"},
		},
		"paths are cleaned": {
			paths: []string{"./apps/web/package.json", "/apps/api//go.mod", "apps/../evil/../apps/jobs/Dockerfile"},
			want:  []string{"web:web:apps/web:/", "api:web:apps/api:/api", "jobs:worker:apps/jobs:"},
		},
		"a workspace one level down gives its apps": {
			paths: []string{"go.mod", "platform/package.json", "platform/apps/web/package.json", "platform/apps/api/go.mod"},
			want:  []string{"web:web:platform/apps/web:/", "api:web:platform/apps/api:/api"},
		},
		"a workspace file makes a folder a workspace": {
			paths: []string{"stack/go.work", "stack/go.mod", "stack/gateway/go.mod", "stack/billing/go.mod"},
			want:  []string{"billing:web:stack/billing:/", "gateway:web:stack/gateway:/gateway"},
		},
		"a workspace file alone, without apps inside, is an app": {
			paths: []string{"site/turbo.json", "site/package.json"},
			want:  []string{"site:web:site:/"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := names(detect.Services(c.paths, c.root)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestServicesSaysWhatItLeftOut(t *testing.T) {
	p := detect.Services([]string{"README.md", "node_modules/a/package.json"}, "")
	if len(p.Services) != 0 || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "No se encontró") {
		t.Fatalf("nothing found: %+v", p)
	}

	var many []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		many = append(many, "apps/"+n+"/package.json")
	}
	p = detect.Services(many, "")
	if len(p.Services) != detect.MaxServices || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "10 carpetas") {
		t.Fatalf("too many: %+v", p)
	}

	p = detect.Services([]string{"package.json", "web/package.json"}, "")
	if len(p.Services) != 1 || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "La raíz también") {
		t.Fatalf("a workspace root: %+v", p)
	}
	if p.Services[0].Evidence != "web/package.json" {
		t.Fatalf("evidence %q", p.Services[0].Evidence)
	}
	// a root that is clearly a workspace (apps in apps/) needs no note
	if p = detect.Services([]string{"package.json", "apps/web/package.json"}, ""); len(p.Notes) != 0 {
		t.Fatalf("a clear workspace: %+v", p)
	}
}

func TestEvidenceIsTheStrongestMarker(t *testing.T) {
	p := detect.Services([]string{"api/package.json", "api/Dockerfile", "api/go.mod"}, "")
	if p.Services[0].Evidence != "api/Dockerfile" {
		t.Fatalf("evidence %q", p.Services[0].Evidence)
	}
	p = detect.Services([]string{"src/api/go.mod"}, "src")
	if p.Services[0].Evidence != "src/api/go.mod" || p.Services[0].Root != "api" {
		t.Fatalf("evidence under root %+v", p.Services[0])
	}
}

func TestReadsAtMostMaxPaths(t *testing.T) {
	paths := make([]string, detect.MaxPaths+1)
	for i := range paths {
		paths[i] = "src/file.txt"
	}
	paths[detect.MaxPaths] = "go.mod"
	p := detect.Services(paths, "")
	if len(p.Services) != 0 || len(p.Notes) != 2 {
		t.Fatalf("got %+v", p)
	}
}

// Files is everything a proposal reads: keeping only the paths that end in
// one of them gives the same proposal (what an assistant sends for a big
// folder).
func TestFilesAreAllAProposalReads(t *testing.T) {
	paths := []string{"pnpm-workspace.yaml", "package.json", "README.md", "apps/web/package.json", "apps/web/src/a.tsx", "apps/web/next.config.js",
		"apps/api/go.mod", "apps/api/main.go", "apps/api/Dockerfile", "apps/jobs/pyproject.toml", "apps/jobs/worker.py", "packages/ui/package.json"}
	var kept []string
	for _, p := range paths {
		for _, f := range detect.Files() {
			if p == f || strings.HasSuffix(p, "/"+f) {
				kept = append(kept, p)
				break
			}
		}
	}
	if len(kept) >= len(paths) {
		t.Fatalf("nothing filtered: %v", kept)
	}
	if all, some := detect.Services(paths, ""), detect.Services(kept, ""); !reflect.DeepEqual(all, some) {
		t.Fatalf("filtered proposal differs:\n%+v\n%+v", all, some)
	}
}
