package agent

import (
	"regexp"
	"testing"

	"github.com/Abraxas-365/odyssey/agent/builder"
)

func TestBuilderIdentity(t *testing.T) {
	base := BuildOptions{Registry: "registry.test/odyssey", Platform: "linux/amd64", BuildKitImage: "bk@sha256:1", Frontend: "fe@sha256:2"}
	id := BuilderIdentity(base)
	if !regexp.MustCompile(`^b1:[0-9a-f]{64}$`).MatchString(id) {
		t.Fatalf("identity %q", id)
	}
	if BuilderIdentity(base) != id {
		t.Fatal("not deterministic")
	}
	// things that do not shape an image do not change it
	same := base
	same.AgentImage, same.SourceURL, same.ScanImage, same.NoScan = "agent:x", "http://src", "trivy", true
	if BuilderIdentity(same) != id {
		t.Fatal("the agent's image, the source URL or the scan changed the identity")
	}
	changes := map[string]func(*BuildOptions){
		"buildkit": func(b *BuildOptions) { b.BuildKitImage = "bk@sha256:9" },
		"frontend": func(b *BuildOptions) { b.Frontend = "fe@sha256:9" },
		"platform": func(b *BuildOptions) { b.Platform = "linux/arm64" },
		"registry": func(b *BuildOptions) { b.Registry = "other.test/odyssey" },
		"insecure": func(b *BuildOptions) { b.InsecureRegistry = true },
	}
	for name, mutate := range changes {
		o := base
		mutate(&o)
		if BuilderIdentity(o) == id {
			t.Errorf("%s does not change the identity", name)
		}
	}
	for name, v := range map[string]*string{"railpack": &builder.RailpackVersion, "agent version": &Version} {
		old := *v
		*v = old + "-other"
		changed := BuilderIdentity(base)
		*v = old
		if changed == id {
			t.Errorf("%s does not change the identity", name)
		}
	}
}
