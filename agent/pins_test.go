package agent

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/agent/builder"
	"golang.org/x/mod/modfile"
)

// TestPinsAgree: the pins that must move together do. A Railpack bump in
// go.mod must bring the frontend image of the same version (it reads the
// plans the library writes) and the mise the library expects (the image
// bakes it so the library never downloads one unchecked).
func TestPinsAgree(t *testing.T) {
	data, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	var railpack string
	for _, r := range mod.Require {
		if r.Mod.Path == "github.com/railwayapp/railpack" {
			railpack = r.Mod.Version
		}
	}
	if railpack == "" {
		t.Fatal("go.mod does not require railpack")
	}
	if !strings.Contains(FrontendImage, ":"+railpack+"@sha256:") {
		t.Errorf("FrontendImage %s is not railpack %s (go.mod)", FrontendImage, railpack)
	}

	dockerfile, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ARG MISE_VERSION=(\S+)$`).FindSubmatch(dockerfile)
	if m == nil {
		t.Fatal("the Dockerfile does not pin MISE_VERSION")
	}
	if got, want := string(m[1]), builder.MiseVersion(); got != want {
		t.Errorf("Dockerfile bakes mise %s; railpack %s expects %s", got, railpack, want)
	}
	if !regexp.MustCompile(`(?m)^FROM golang:` + regexp.QuoteMeta(mod.Go.Version) + `-alpine AS build$`).Match(dockerfile) {
		t.Errorf("the Dockerfile's Go image is not go.mod's (%s)", mod.Go.Version)
	}
}

// TestDockerfileCopiesEveryGoTree: the image builds from the top-level
// directories it copies, so one that holds a package the binaries import and
// is missing from the build stage breaks `docker build` while `go build` on a
// checkout still passes (skills/ did, once).
func TestDockerfileCopiesEveryGoTree(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	dockerfile, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	const module = "github.com/Abraxas-365/odyssey/"
	cmd := exec.Command("go", "list", "-deps", "./cmd/...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	trees := map[string]bool{}
	for _, pkg := range strings.Fields(string(out)) {
		if rest, ok := strings.CutPrefix(pkg, module); ok {
			trees[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	if !trees["cmd"] || !trees["internal"] {
		t.Fatalf("go list found no binaries: %v", trees)
	}
	for name := range trees {
		if !regexp.MustCompile(`(?m)^COPY ` + regexp.QuoteMeta(name) + `/ ` + regexp.QuoteMeta(name) + `/$`).Match(dockerfile) {
			t.Errorf("the binaries import %s/ but the Dockerfile's build stage does not COPY it", name)
		}
	}
}
