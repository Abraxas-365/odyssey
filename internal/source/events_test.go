package source_test

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// TestEventContracts covers source.installation_changed (the older source
// events' contracts are exercised by sourcesvc's tests).
func TestEventContracts(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	inst := source.Installation{ID: source.MustParseInstallationID("00000000-0000-4000-8000-0000000000f1"), Organization: org,
		ExternalID: 52104471, Account: "inedge-andina", AllRepositories: true, Suspended: true}
	eventtest.Golden(t, source.NewInstallationChanged(inst, source.ActionSuspend))
}

func TestInstallationChangedSummaries(t *testing.T) {
	inst := source.Installation{ID: source.NewInstallationID(), Organization: identity.NewOrganizationID(), Account: "acme"}
	for action, want := range map[string]string{
		source.ActionSuspend:   "GitHub (acme) quedó suspendido",
		source.ActionUnsuspend: "GitHub (acme) se reanudó",
		"added":                "GitHub (acme) cambió sus repositorios",
	} {
		if got := source.NewInstallationChanged(inst, action).Summary; got != want {
			t.Errorf("%s: summary %q, want %q", action, got, want)
		}
	}
}

func TestStatusTexts(t *testing.T) {
	long := strings.Repeat("ñ", 300)
	for _, s := range []source.CommitStatus{source.PushFailedStatus(long), source.DeploymentFailedStatus(long)} {
		if n := len([]rune(s.Description)); n != source.MaxStatusDescription {
			t.Errorf("description of %d runes, want %d", n, source.MaxStatusDescription)
		}
	}
	for _, c := range []struct {
		got   source.CommitStatus
		state string
		text  string
	}{
		{source.PreparingStatus(), source.CommitPending, "Preparando la publicación"},
		{source.PublishingStatus(), source.CommitPending, "Publicando"},
		{source.LiveStatus(), source.CommitSuccess, "En línea"},
		{source.DeploymentFailedStatus("boom"), source.CommitFailure, "No se pudo publicar: boom"},
		{source.ReplacedStatus(), source.CommitError, "Reemplazada por una publicación más nueva"},
		{source.PushFailedStatus("x"), source.CommitFailure, "x"},
	} {
		if c.got.State != c.state || c.got.Description != c.text {
			t.Errorf("status = %+v, want %s %q", c.got, c.state, c.text)
		}
	}
}
