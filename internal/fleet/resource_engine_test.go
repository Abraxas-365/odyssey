package fleet_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// codeOf is err's errx code ("" none).
func codeOf(err error) string {
	if e, ok := err.(*errx.Error); ok {
		return e.Code
	}
	return ""
}

// Capabilities are judged per engine: a built-in resource needs the kind's
// engine; one the company already has needs the existing engine and every
// store it reads, never the kind's built-in engine.
func TestCapabilitiesPerEngine(t *testing.T) {
	existing := resourcespec.ExamplePostgres("vault")
	none := fleet.Capabilities{ResourceKinds: []string{"redis", "files"}}
	connects := fleet.Capabilities{ResourceKinds: []string{"redis"}, ResourceCapabilities: []string{resourcespec.ConnectExisting},
		SecretStores: []string{"vault", "vault"}}
	if got := connects.Resources(); !slices.Equal(got, []string{"builtin:redis", resourcespec.ConnectExisting, "secretstore:vault"}) {
		t.Fatalf("resources %v", got)
	}
	// a cluster without CloudNativePG connects the company's database
	if err := connects.CheckResource(resourcekind.Postgres, existing); err != nil {
		t.Fatalf("existing on a cluster without the built-in engine: %v", err)
	}
	if !connects.CanConnect(resourcekind.Postgres) || !connects.CanConnect(resourcekind.ObjectStorage) || connects.CanConnect(resourcekind.Files) {
		t.Fatal("CanConnect")
	}
	if connects.CheckResources([]fleet.ResourceRef{{Name: "db", Kind: resourcekind.Postgres}}) != nil {
		t.Fatal("an app connected to the company's database refused")
	}
	// but not a built-in database
	if err := connects.CheckResource(resourcekind.Postgres, resourcespec.Spec{}); codeOf(err) != fleet.CodeResourcesUnavailable {
		t.Fatalf("builtin without the engine: %v", err)
	}
	// no existing engine: the agent or External Secrets is missing
	err := none.CheckResource(resourcekind.Postgres, existing)
	if codeOf(err) != fleet.CodeResourcesUnsupported || !strings.Contains(err.Error(), "External Secrets") {
		t.Fatalf("no existing engine: %v", err)
	}
	// a store the cluster lost: named, in a fixed phrase
	lost := connects
	lost.SecretStores = []string{"aws-prod"}
	err = lost.CheckResource(resourcekind.Postgres, existing)
	if codeOf(err) != fleet.CodeResourcesUnsupported || !strings.Contains(err.Error(), "«vault»") {
		t.Fatalf("lost store: %v", err)
	}
}

// A resource the company already has has no size; every other one does
// (one being deleted may carry none).
func TestDesiredResourceSizeFollowsTheEngine(t *testing.T) {
	base := fleet.DesiredResource{Organization: org, Project: proj, Environment: env, Name: "db", Kind: resourcekind.Postgres,
		Generation: 1, StorageGi: 5, Recipe: "postgres-basico@1"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	ex := base
	ex.StorageGi, ex.Recipe, ex.Spec = 0, "org-reportes@1", resourcespec.ExamplePostgres("vault")
	if err := ex.Validate(); err != nil {
		t.Fatalf("existing without size: %v", err)
	}
	for name, change := range map[string]func(*fleet.DesiredResource){
		"existing with size": func(r *fleet.DesiredResource) { r.StorageGi = 5 },
		"builtin without":    func(r *fleet.DesiredResource) { r.Spec = resourcespec.Spec{} },
		"negative":           func(r *fleet.DesiredResource) { r.StorageGi, r.Spec = -1, resourcespec.Spec{} },
	} {
		r := ex
		change(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
