package mcpx_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/mcpx"
)

type pendingOK struct {
	mcpx.Pending
	App string `json:"app"`
}

type pendingShadowed struct {
	mcpx.Pending
	App    string `json:"app"`
	Status string `json:"status"`
}

type pendingShadowedByNested struct {
	mcpx.Pending
	Inner struct{ X int } `json:"inner"`
	Other struct {
		RequestID string `json:"request_id"`
	} `json:"other"`
	RequestID string `json:"request_id,omitempty"`
}

type noPending struct {
	Status string `json:"status"`
}

// TestPendingConflicts proves the detector on fixtures: the real tools are
// checked in bootstrap (TestMCPOutputsDoNotShadowPending).
func TestPendingConflicts(t *testing.T) {
	if got := mcpx.PendingConflicts(reflect.TypeOf(pendingOK{})); len(got) != 0 {
		t.Errorf("clean output: %v", got)
	}
	if got := mcpx.PendingConflicts(reflect.TypeOf(&noPending{})); len(got) != 0 {
		t.Errorf("no Pending: %v", got)
	}
	if got := mcpx.PendingConflicts(reflect.TypeOf(pendingShadowed{})); !slices.Equal(got, []string{"status"}) {
		t.Errorf("shadowed status: %v", got)
	}
	if got := mcpx.PendingConflicts(reflect.TypeOf(pendingShadowedByNested{})); !slices.Equal(got, []string{"request_id"}) {
		t.Errorf("shadowed request_id: %v", got)
	}
}
