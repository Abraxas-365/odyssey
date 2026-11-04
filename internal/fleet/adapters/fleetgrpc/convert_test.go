package fleetgrpc

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/project"
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

// The agent's failure reason travels untouched (Report.Valid judges it), so
// "resource" reaches the control plane.
func TestReportFromProtoCarriesTheReason(t *testing.T) {
	env := project.NewEnvironmentID().String()
	r, err := reportFromProto(&fleetv1.AppReport{Apps: []*fleetv1.AppStatus{{EnvironmentId: env, Name: "web", Phase: "failed",
		Reason: "resource", Message: "resource db is not ready: no storage"}}})
	if err != nil || len(r.Apps) != 1 || r.Apps[0].Reason != fleet.ReasonResource {
		t.Fatalf("reason lost: %+v %v", r, err)
	}
	if _, err := r.Valid(); err != nil {
		t.Errorf("the resource reason was refused: %v", err)
	}
}

// An unreadable report is a validation error: the server logs it and keeps
// the tunnel (any other type closes it).
func TestUnreadableReportsAreValidationErrors(t *testing.T) {
	_, err := resourceReportFromProto(&fleetv1.ResourceReport{Resources: []*fleetv1.ResourceStatus{{EnvironmentId: "not-a-uuid", Name: "db"}}})
	if !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("resource report: %T %v, want a validation error", err, err)
	}
	_, err = reportFromProto(&fleetv1.AppReport{Apps: []*fleetv1.AppStatus{{EnvironmentId: "not-a-uuid", Name: "web"}}})
	if !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("app report: %T %v, want a validation error", err, err)
	}
}
