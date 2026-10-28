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
// (internal/, the other cmd/) are separate programs that run in different
// places and talk only through the gRPC contract in proto/:
//
//   - the agent never imports the control plane;
//   - the control plane never imports the agent, nor Kubernetes: since
//     phase 1a-a3 it holds no cluster credentials; the agent applies what
//     it is sent through its tunnel;
//   - proto/ imports neither plane;
//   - in the control plane the wire format (proto/, protobuf) is used only
//     by adapters (fleet/adapters/fleetgrpc), and gRPC only by adapters and
//     the composition root (which builds the gateway's server).
//
// End-to-end tests (adapter and composition-root tests) may start an agent.

const repoModule = "github.com/Abraxas-365/odyssey/"

var (
	kubernetesLibs = []string{"k8s.io/", "sigs.k8s.io/"}
	wireFormat     = []string{repoModule + "proto/", "google.golang.org/protobuf"}
	grpcLib        = "google.golang.org/grpc"
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

// planeViolations checks repository-relative files against the rules above.
func planeViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		agentSide := strings.HasPrefix(f.rel, "agent/") || strings.HasPrefix(f.rel, "cmd/odyssey-agent/")
		contract := strings.HasPrefix(f.rel, "proto/")
		controlPlane := !agentSide && (strings.HasPrefix(f.rel, "internal/") || strings.HasPrefix(f.rel, "cmd/"))
		adapter := strings.HasPrefix(f.rel, "internal/") && strings.Contains(f.rel, "/adapters/")
		wiring := strings.HasPrefix(f.rel, "internal/bootstrap/")
		e2e := (adapter || wiring) && strings.HasSuffix(f.rel, "_test.go")
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pos := f.fset.Position(imp.Pos()).String()
			if agentSide && (strings.HasPrefix(p, repoModule+"internal/") || strings.HasPrefix(p, repoModule+"cmd/")) {
				out = append(out, pos+": the agent must not import the control plane ("+p+"); they talk through proto/")
			}
			if contract && strings.HasPrefix(p, repoModule) && !strings.HasPrefix(p, repoModule+"proto/") {
				out = append(out, pos+": proto/ is the contract between the planes and imports neither ("+p+")")
			}
			if !controlPlane || e2e {
				continue
			}
			if strings.HasPrefix(p, repoModule+"agent/") || p == repoModule+"agent" {
				out = append(out, pos+": the control plane must not import the agent ("+p+"); they talk through proto/")
			}
			for _, lib := range kubernetesLibs {
				if strings.HasPrefix(p, lib) {
					out = append(out, pos+": "+p+" is Kubernetes: the control plane never talks to a cluster (its agent does)")
				}
			}
			for _, lib := range wireFormat {
				if strings.HasPrefix(p, lib) && !adapter {
					out = append(out, pos+": "+p+" is wire format: only adapters (e.g. fleetgrpc) may import it")
				}
			}
			if (p == grpcLib || strings.HasPrefix(p, grpcLib+"/")) && !adapter && !wiring {
				out = append(out, pos+": "+p+" is transport: only adapters and the composition root may import it")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestPlanesStayApart(t *testing.T) {
	report(t, planeViolations(repoSources(t, "agent", "cmd", "internal", "proto")))
}

func TestPlaneRulesCatchViolations(t *testing.T) {
	m := repoModule
	files := parseFixtures(t, map[string]string{
		// correct code
		"agent/controllers/app/a.go":                       `package app; import (_ "sigs.k8s.io/controller-runtime"; _ "` + m + `agent/api/v1alpha1")`,
		"agent/tunnel/t.go":                                `package tunnel; import (_ "google.golang.org/grpc"; _ "` + m + `proto/fleet/v1")`,
		"internal/fleet/adapters/fleetgrpc/s.go":           `package fleetgrpc; import (_ "google.golang.org/grpc"; _ "google.golang.org/protobuf/proto"; _ "` + m + `proto/fleet/v1"; _ "` + m + `internal/fleet")`,
		"internal/bootstrap/gateway.go":                    `package bootstrap; import (_ "google.golang.org/grpc/credentials"; _ "` + m + `internal/fleet/adapters/fleetgrpc")`,
		"internal/bootstrap/fleet_test.go":                 `package bootstrap_test; import (_ "` + m + `agent/tunnel"; _ "` + m + `proto/fleet/v1")`,
		"internal/bootstrap/kube_test.go":                  `package bootstrap_test; import (_ "` + m + `agent"; _ "k8s.io/client-go/tools/clientcmd")`,
		"internal/fleet/adapters/fleetgrpc/server_test.go": `package fleetgrpc_test; import _ "` + m + `agent/tunnel"`,
		"proto/fleet/v1/fleet.pb.go":                       `package fleetv1; import _ "google.golang.org/protobuf/reflect/protoreflect"`,
		"cmd/odyssey-gateway/main.go":                      `package main; import _ "` + m + `internal/bootstrap"`,
		"cmd/odyssey-agent/main.go":                        `package main; import _ "` + m + `agent"`,
		// violations
		"agent/agent.go":                             `package agent; import _ "` + m + `internal/fleet"`,
		"proto/fleet/v1/extra.go":                    `package fleetv1; import _ "` + m + `agent/api/v1alpha1"`,
		"internal/release/adapters/releasekube/c.go": `package releasekube; import _ "k8s.io/client-go/rest"`,
		"internal/release/adapters/releasex/x.go":    `package releasex; import _ "` + m + `agent/api/v1alpha1"`,
		"internal/fleet/fleetsvc/s.go":               `package fleetsvc; import _ "` + m + `proto/fleet/v1"`,
		"internal/fleet/fleetmodule/m.go":            `package fleetmodule; import _ "google.golang.org/grpc"`,
		"internal/bootstrap/wire.go":                 `package bootstrap; import _ "` + m + `proto/fleet/v1"`,
		"cmd/odyssey-worker/main.go":                 `package main; import _ "sigs.k8s.io/controller-runtime/pkg/client"`,
		"internal/fleet/fleet_test.go":               `package fleet_test; import _ "` + m + `agent/tunnel"`,
	})
	expectViolations(t, planeViolations(files),
		"agent/agent.go: the agent must not import the control plane",
		"proto/fleet/v1/extra.go: proto/ is the contract between the planes",
		"internal/release/adapters/releasekube/c.go: the control plane never talks to a cluster",
		"internal/release/adapters/releasex/x.go: the control plane must not import the agent",
		"internal/fleet/fleetsvc/s.go: is wire format",
		"internal/fleet/fleetmodule/m.go: is transport",
		"internal/bootstrap/wire.go: is wire format",
		"cmd/odyssey-worker/main.go: the control plane never talks to a cluster",
		"internal/fleet/fleet_test.go: the control plane must not import the agent",
	)
}
