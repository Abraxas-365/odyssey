package bootstrap_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The data plane (agent/, cmd/odyssey-agent) and the control plane
// (internal/, cmd/odyssey-api, cmd/odyssey-worker) are separate programs
// that will run in different places: the agent never imports the control
// plane, and the control plane sees of the agent only agent/api (the CRD
// types, later proto/). Inside the control plane, Kubernetes is an external
// system like any other: its client libraries and the CRD types are used
// only by adapters (release/adapters/releasekube today).

const repoModule = "github.com/Abraxas-365/odyssey/"

var (
	kubernetesLibs = []string{"k8s.io/", "sigs.k8s.io/", repoModule + "agent/api/"}
	agentAPI       = repoModule + "agent/api/"
)

// repoSources parses the .go files (tests included) of the given top-level
// directories, with paths relative to the repository root.
func repoSources(t *testing.T, dirs ...string) []goFile {
	t.Helper()
	fset := token.NewFileSet()
	var out []goFile
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join("..", "..", dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join("..", ".."), p)
			out = append(out, goFile{rel: filepath.ToSlash(rel), file: f, fset: fset})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// planeViolations checks repository-relative files: agent/ and
// cmd/odyssey-agent import nothing of the control plane; the control plane
// imports of agent/ only agent/api, and Kubernetes libraries (agent/api
// included) only in internal adapters (tests included: the e2e test of an
// adapter may start the agent).
func planeViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		agentSide := strings.HasPrefix(f.rel, "agent/") || strings.HasPrefix(f.rel, "cmd/odyssey-agent/")
		controlPlane := !agentSide && (strings.HasPrefix(f.rel, "internal/") || strings.HasPrefix(f.rel, "cmd/"))
		adapter := strings.HasPrefix(f.rel, "internal/") && strings.Contains(f.rel, "/adapters/")
		e2e := adapter && strings.HasSuffix(f.rel, "_test.go")
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pos := f.fset.Position(imp.Pos()).String()
			if agentSide && (strings.HasPrefix(p, repoModule+"internal/") || strings.HasPrefix(p, repoModule+"cmd/")) {
				out = append(out, pos+": the agent must not import the control plane ("+p+"); share types through agent/api")
			}
			if controlPlane && !e2e && strings.HasPrefix(p, repoModule+"agent") && !strings.HasPrefix(p, agentAPI) {
				out = append(out, pos+": the control plane sees only agent/api of the agent, not "+p)
			}
			if controlPlane && !adapter {
				for _, lib := range kubernetesLibs {
					if strings.HasPrefix(p, lib) {
						out = append(out, pos+": "+p+" is Kubernetes: only adapters (e.g. releasekube) may import it")
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestPlanesStayApart(t *testing.T) {
	report(t, planeViolations(repoSources(t, "agent", "cmd", "internal")))
}

func TestPlaneRulesCatchViolations(t *testing.T) {
	m := repoModule
	files := parseFixtures(t, map[string]string{
		// correct code
		"agent/controllers/app/a.go":                         `package app; import (_ "sigs.k8s.io/controller-runtime"; _ "` + m + `agent/api/v1alpha1")`,
		"internal/release/adapters/releasekube/c.go":         `package releasekube; import (_ "k8s.io/client-go/rest"; _ "` + m + `agent/api/v1alpha1"; _ "` + m + `internal/release")`,
		"cmd/odyssey-api/main.go":                            `package main; import _ "` + m + `internal/bootstrap"`,
		"cmd/odyssey-agent/main.go":                          `package main; import _ "` + m + `agent"`,
		"internal/release/adapters/releasekube/kube_test.go": `package releasekube_test; import _ "` + m + `agent/controllers/app"`,
		// violations
		"agent/agent.go":                             `package agent; import _ "` + m + `internal/release"`,
		"internal/release/cluster.go":                `package release; import _ "k8s.io/api/core/v1"`,
		"internal/release/releasesvc/s.go":           `package releasesvc; import _ "` + m + `agent/api/v1alpha1"`,
		"internal/release/releasemodule/m.go":        `package releasemodule; import _ "sigs.k8s.io/controller-runtime/pkg/client"`,
		"internal/release/adapters/releasekube/x.go": `package releasekube; import _ "` + m + `agent/controllers/app"`,
		"cmd/odyssey-worker/main.go":                 `package main; import _ "k8s.io/client-go/rest"`,
	})
	expectViolations(t, planeViolations(files),
		"agent/agent.go: the agent must not import the control plane",
		"internal/release/cluster.go: k8s.io/api/core/v1 is Kubernetes",
		"internal/release/releasesvc/s.go: agent/api/v1alpha1 is Kubernetes",
		"internal/release/releasemodule/m.go: sigs.k8s.io/controller-runtime/pkg/client is Kubernetes",
		"internal/release/adapters/releasekube/x.go: the control plane sees only agent/api",
		"cmd/odyssey-worker/main.go: k8s.io/client-go/rest is Kubernetes",
	)
}
