package bootstrap_test

// Attention contract: the kinds and sources "Necesita tu atención" can say are
// a closed list in the domain (inventory.Kinds, the Source constants) and in
// api/openapi.yaml. The console words each one (a Record over the generated
// enum: a kind it does not word fails its typecheck), so a kind added to the
// domain and not to the contract is one the console would show without words.

import (
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/inventory"
)

func TestAttentionKindsAreTheContracts(t *testing.T) {
	model, _ := loadSpec(t)
	item := model.Components.Schemas.GetOrZero("AttentionItem")
	att := model.Components.Schemas.GetOrZero("Attention")
	if item == nil || att == nil {
		t.Fatal("AttentionItem / Attention missing from api/openapi.yaml")
	}

	kinds := []string{}
	for _, n := range item.Schema().Properties.GetOrZero("kind").Schema().Enum {
		kinds = append(kinds, n.Value)
	}
	want := []string{}
	for _, k := range inventory.Kinds {
		want = append(want, string(k))
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("AttentionItem.kind enum %v, inventory.Kinds %v (same list, same order)", kinds, want)
	}

	sources := []string{}
	for _, n := range att.Schema().Properties.GetOrZero("unavailable").Schema().Items.A.Schema().Enum {
		sources = append(sources, n.Value)
	}
	wantSources := []string{string(inventory.SourceApprovals), string(inventory.SourceApps), string(inventory.SourceClusters),
		string(inventory.SourceRequests), string(inventory.SourceGitHub)}
	if !slices.Equal(sources, wantSources) {
		t.Errorf("Attention.unavailable enum %v, inventory sources %v", sources, wantSources)
	}
}
