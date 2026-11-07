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
//   - pkg/ holds code both planes run with the same rules (srcarchive: the
//     source checks of the upload and of the builder); it imports neither
//     plane nor Kubernetes;
//   - pkg/odiceaauth is what app developers import into their own apps: it
//     takes only the standard library (its tests add only the package
//     itself), so adding it costs an app no dependency;
//   - agent/builder is the program of a build's pod (which has no service
//     account token and talks to nothing but the agent's source server and
//     the registry): it imports no Kubernetes, no gRPC/proto and no other
//     agent package (pkg/ only); Railpack is imported only there;
//   - agent/access is the sign-in gate (a Deployment of its own, in front of
//     the apps): it speaks to the control plane only through the ticket
//     contract (docs/access.md), so it imports no proto/gRPC, no
//     controller-runtime and no other agent package; Kubernetes only through
//     client-go (its cookie-key Secret);
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
	railpackLib    = "github.com/railwayapp/railpack"
)

// builderViolations: what a build pod's program may not import.
func builderViolations(rel, p, pos string) []string {
	var out []string
	builder := strings.HasPrefix(rel, "agent/builder/")
	if strings.HasPrefix(p, railpackLib) && !builder {
		out = append(out, pos+": Railpack is the build pod's planner: only agent/builder imports it ("+p+")")
	}
	if !builder {
		return out
	}
	if strings.HasPrefix(p, repoModule) && !strings.HasPrefix(p, repoModule+"pkg/") {
		out = append(out, pos+": agent/builder runs in a build's pod and imports only pkg/ ("+p+")")
	}
	for _, lib := range append(append([]string{grpcLib}, kubernetesLibs...), "google.golang.org/protobuf") {
		if strings.HasPrefix(p, lib) {
			out = append(out, pos+": agent/builder runs in a build's pod, which talks to no cluster API nor the control plane ("+p+")")
		}
	}
	return out
}

// accessViolations: what the sign-in gate may not import.
func accessViolations(rel, p, pos string) []string {
	if !strings.HasPrefix(rel, "agent/access/") {
		return nil
	}
	var out []string
	if strings.HasPrefix(p, repoModule) && !strings.HasPrefix(p, repoModule+"pkg/") {
		out = append(out, pos+": agent/access meets the control plane only through the ticket contract and imports only pkg/ ("+p+")")
	}
	for _, lib := range []string{grpcLib, "google.golang.org/protobuf", "sigs.k8s.io/"} {
		if strings.HasPrefix(p, lib) {
			out = append(out, pos+": agent/access takes no wire format nor controller-runtime ("+p+")")
		}
	}
	return out
}

// secretstoreViolations: what the managed-secret store may not import. It
// opens a sealed value and writes a Secret, so it stays small and auditable:
// from this repository only agent/desired, pkg/secretbox and the fleet
// proto; from outside it the standard library, Kubernetes and
// controller-runtime (its tests may use any third-party library).
func secretstoreViolations(rel, p, pos string) []string {
	if !strings.HasPrefix(rel, "agent/secretstore/") {
		return nil
	}
	if strings.HasPrefix(p, repoModule) {
		for _, ok := range []string{"agent/desired", "pkg/secretbox", "proto/fleet/v1"} {
			if p == repoModule+ok || strings.HasPrefix(p, repoModule+ok+"/") {
				return nil
			}
		}
		return []string{pos + ": agent/secretstore imports only agent/desired, pkg/secretbox and the fleet proto from this repository (" + p + ")"}
	}
	first, _, _ := strings.Cut(p, "/")
	stdlib := !strings.Contains(first, ".")
	if stdlib || strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(p, "k8s.io/") || strings.HasPrefix(p, "sigs.k8s.io/") {
		return nil
	}
	return []string{pos + ": agent/secretstore takes only the standard library, Kubernetes and controller-runtime from outside this repository (" + p + ")"}
}

// stdlibOnly are the pkg/ packages that take only the standard library (their
// tests may also import the package itself), and why.
var stdlibOnly = map[string]string{
	"pkg/odiceaauth": "is imported by apps",
	"pkg/detect":     "is a pure proposal from file paths (no I/O, no dependency)",
}

// stdlibOnlyViolations: pkg/odiceaauth is imported by the apps Odicea runs
// and pkg/detect proposes an app's services from a list of paths, so both
// take only the standard library; their tests may also import the package
// itself (external test package).
func stdlibOnlyViolations(rel, p, pos string) []string {
	for pkg, why := range stdlibOnly {
		if !strings.HasPrefix(rel, pkg+"/") {
			continue
		}
		if p == repoModule+pkg && strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		first, _, _ := strings.Cut(p, "/")
		if strings.Contains(first, ".") {
			return []string{pos + ": " + pkg + " " + why + " and takes only the standard library (" + p + ")"}
		}
	}
	return nil
}

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
		shared := strings.HasPrefix(f.rel, "pkg/")
		controlPlane := !agentSide && (strings.HasPrefix(f.rel, "internal/") || strings.HasPrefix(f.rel, "cmd/"))
		adapter := strings.HasPrefix(f.rel, "internal/") && strings.Contains(f.rel, "/adapters/")
		wiring := strings.HasPrefix(f.rel, "internal/bootstrap/")
		e2e := (adapter || wiring) && strings.HasSuffix(f.rel, "_test.go")
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pos := f.fset.Position(imp.Pos()).String()
			out = append(out, builderViolations(f.rel, p, pos)...)
			out = append(out, accessViolations(f.rel, p, pos)...)
			out = append(out, secretstoreViolations(f.rel, p, pos)...)
			out = append(out, stdlibOnlyViolations(f.rel, p, pos)...)
			if agentSide && (strings.HasPrefix(p, repoModule+"internal/") || strings.HasPrefix(p, repoModule+"cmd/")) {
				out = append(out, pos+": the agent must not import the control plane ("+p+"); they talk through proto/")
			}
			if contract && strings.HasPrefix(p, repoModule) && !strings.HasPrefix(p, repoModule+"proto/") {
				out = append(out, pos+": proto/ is the contract between the planes and imports neither ("+p+")")
			}
			if shared && strings.HasPrefix(p, repoModule) && !strings.HasPrefix(p, repoModule+"pkg/") {
				out = append(out, pos+": pkg/ is shared by both planes and imports neither ("+p+")")
			}
			for _, lib := range kubernetesLibs {
				if shared && strings.HasPrefix(p, lib) {
					out = append(out, pos+": pkg/ is shared by both planes and may not use Kubernetes ("+p+")")
				}
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
	report(t, planeViolations(repoSources(t, "agent", "cmd", "internal", "pkg", "proto")))
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
		"pkg/srcarchive/s.go":                              `package srcarchive; import _ "archive/tar"`,
		"pkg/odiceaauth/a.go":                              `package odiceaauth; import (_ "crypto/ed25519"; _ "net/http")`,
		"pkg/odiceaauth/a_test.go":                         `package odiceaauth_test; import (_ "testing"; _ "` + m + `pkg/odiceaauth")`,
		"pkg/detect/d.go":                                  `package detect; import (_ "path"; _ "slices")`,
		"pkg/detect/d_test.go":                             `package detect_test; import (_ "testing"; _ "` + m + `pkg/detect")`,
		"agent/builder/b.go":                               `package builder; import (_ "` + m + `pkg/srcarchive"; _ "github.com/railwayapp/railpack/core")`,
		"agent/builds/m.go":                                `package builds; import (_ "` + m + `agent/controllers/build"; _ "` + m + `pkg/srcarchive"; _ "` + m + `proto/fleet/v1"; _ "sigs.k8s.io/controller-runtime/pkg/client")`,
		"agent/controllers/build/j.go":                     `package build; import (_ "` + m + `agent/builder"; _ "k8s.io/api/batch/v1")`,
		"internal/build/archive/a.go":                      `package archive; import _ "` + m + `pkg/srcarchive"`,
		"agent/access/g.go":                                `package access; import (_ "github.com/golang-jwt/jwt/v5"; _ "k8s.io/client-go/kubernetes")`,
		"agent/secretstore/s.go":                           `package secretstore; import (_ "encoding/json"; _ "` + m + `agent/desired"; _ "` + m + `pkg/secretbox"; _ "` + m + `proto/fleet/v1"; _ "k8s.io/api/core/v1"; _ "sigs.k8s.io/controller-runtime/pkg/client")`,
		"agent/secretstore/s_test.go":                      `package secretstore; import (_ "github.com/google/uuid"; _ "google.golang.org/protobuf/proto")`,
		// violations
		"agent/secretstore/bad.go":                   `package secretstore; import (_ "` + m + `agent/controllers/app"; _ "` + m + `pkg/srcarchive"; _ "github.com/google/uuid"; _ "google.golang.org/grpc")`,
		"agent/access/bad.go":                        `package access; import (_ "` + m + `proto/fleet/v1"; _ "sigs.k8s.io/controller-runtime/pkg/client"; _ "` + m + `agent/api/v1alpha1")`,
		"agent/builder/k.go":                         `package builder; import (_ "k8s.io/client-go/kubernetes"; _ "` + m + `agent/api/v1alpha1"; _ "google.golang.org/grpc")`,
		"agent/builds/r.go":                          `package builds; import _ "github.com/railwayapp/railpack/core/app"`,
		"pkg/srcarchive/bad.go":                      `package srcarchive; import _ "` + m + `internal/errx"`,
		"pkg/odiceaauth/bad.go":                      `package odiceaauth; import (_ "github.com/golang-jwt/jwt/v5"; _ "` + m + `pkg/secretbox")`,
		"pkg/odiceaauth/bad_test.go":                 `package odiceaauth_test; import (_ "github.com/google/uuid"; _ "` + m + `pkg/secretbox")`,
		"pkg/detect/bad.go":                          `package detect; import (_ "github.com/bmatcuk/doublestar/v4"; _ "` + m + `pkg/srcarchive")`,
		"pkg/other/k.go":                             `package other; import (_ "` + m + `agent/api/v1alpha1"; _ "k8s.io/api/core/v1")`,
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
		"pkg/srcarchive/bad.go: pkg/ is shared by both planes and imports neither",
		"pkg/odiceaauth/bad.go: pkg/odiceaauth is imported by apps and takes only the standard library (github.com/golang-jwt/jwt/v5",
		"pkg/odiceaauth/bad.go: pkg/odiceaauth is imported by apps and takes only the standard library (github.com/Abraxas-365/odyssey/pkg/secretbox",
		"pkg/odiceaauth/bad_test.go: pkg/odiceaauth is imported by apps and takes only the standard library (github.com/google/uuid",
		"pkg/odiceaauth/bad_test.go: pkg/odiceaauth is imported by apps and takes only the standard library (github.com/Abraxas-365/odyssey/pkg/secretbox",
		"pkg/detect/bad.go: pkg/detect is a pure proposal from file paths (no I/O, no dependency) and takes only the standard library (github.com/bmatcuk",
		"pkg/detect/bad.go: pkg/detect is a pure proposal from file paths (no I/O, no dependency) and takes only the standard library (github.com/Abraxas-365/odyssey/pkg/srcarchive",
		"pkg/other/k.go: pkg/ is shared by both planes and imports neither",
		"pkg/other/k.go: pkg/ is shared by both planes and may not use Kubernetes",
		"agent/builder/k.go: agent/builder runs in a build's pod and imports only pkg/",
		"agent/builder/k.go: talks to no cluster API nor the control plane (k8s.io/client-go",
		"agent/builder/k.go: talks to no cluster API nor the control plane (google.golang.org/grpc",
		"agent/builds/r.go: Railpack is the build pod's planner",
		"agent/access/bad.go: agent/access meets the control plane only through the ticket contract and imports only pkg/ (github.com/Abraxas-365/odyssey/proto",
		"agent/access/bad.go: agent/access meets the control plane only through the ticket contract and imports only pkg/ (github.com/Abraxas-365/odyssey/agent/api",
		"agent/access/bad.go: agent/access takes no wire format nor controller-runtime (sigs.k8s.io/",
		"agent/secretstore/bad.go: agent/secretstore imports only agent/desired, pkg/secretbox and the fleet proto from this repository (github.com/Abraxas-365/odyssey/agent/controllers/app",
		"agent/secretstore/bad.go: agent/secretstore imports only agent/desired, pkg/secretbox and the fleet proto from this repository (github.com/Abraxas-365/odyssey/pkg/srcarchive",
		"agent/secretstore/bad.go: agent/secretstore takes only the standard library, Kubernetes and controller-runtime from outside this repository (github.com/google/uuid",
		"agent/secretstore/bad.go: agent/secretstore takes only the standard library, Kubernetes and controller-runtime from outside this repository (google.golang.org/grpc",
	)
}
