package fleetgrpc

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/fleet"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
)

func TestAppToProtoCarriesAccess(t *testing.T) {
	for _, mode := range []string{"", fleet.AccessPublic, fleet.AccessTeam, fleet.AccessOrganization} {
		if got := appToProto(fleet.App{Name: "web", Access: mode}).GetAccess().GetMode(); got != mode {
			t.Errorf("access %q arrived as %q", mode, got)
		}
	}
}

func TestAgentFromProtoCarriesTheAccessCapability(t *testing.T) {
	with := &fleetv1.Hello{Capabilities: &fleetv1.Capabilities{Apis: []string{"traefik.io", fleet.CapabilityAccess}}}
	if !agentFromProto(with).Capabilities.CanProtect() {
		t.Error("odicea.access in apis must reach fleet.Capabilities")
	}
	old := &fleetv1.Hello{Capabilities: &fleetv1.Capabilities{Apis: []string{"traefik.io"}}}
	if agentFromProto(old).Capabilities.CanProtect() || agentFromProto(&fleetv1.Hello{}).Capabilities.CanProtect() {
		t.Error("an agent that does not list it cannot protect")
	}
}
