package release_test

import (
	"testing"

	"github.com/Abraxas-365/odyssey/internal/release"
)

// AccessInForce says who can open an app online as the cluster enforces it:
// never a mode it cannot tell, never the stored one as if it were applied.
func TestAccessInForce(t *testing.T) {
	r := func(n int64) *int64 { return &n }
	m := func(a release.AccessMode) *release.AccessMode { return &a }
	team, org, public := release.AccessTeam, release.AccessOrganization, release.AccessPublic
	cases := []struct {
		name        string
		online      bool
		applied     *int64
		appliedMode *release.AccessMode
		latest      *int64
		latestMode  *release.AccessMode
		wantNil     bool
		wantMode    release.AccessMode
		wantPending *release.AccessMode
	}{
		{name: "nothing online", online: false, applied: r(1), appliedMode: m(org), wantNil: true},
		{name: "cluster never told", online: true, wantNil: true},
		{name: "defaults applied", online: true, applied: r(0), wantMode: team},
		{name: "defaults applied, a change stored", online: true, applied: r(0), latest: r(1), latestMode: m(public), wantMode: team, wantPending: &public},
		{name: "applied revision", online: true, applied: r(2), appliedMode: m(org), latest: r(2), latestMode: m(org), wantMode: org},
		{name: "applied revision gone", online: true, applied: r(2), wantNil: true},
		{name: "newer, different", online: true, applied: r(1), appliedMode: m(org), latest: r(3), latestMode: m(public), wantMode: org, wantPending: &public},
		{name: "newer, same mode", online: true, applied: r(1), appliedMode: m(team), latest: r(2), latestMode: m(team), wantMode: team},
	}
	for _, c := range cases {
		got := release.AccessInForce(c.online, c.applied, c.appliedMode, c.latest, c.latestMode)
		switch {
		case c.wantNil:
			if got != nil {
				t.Errorf("%s: %+v, want nil", c.name, got)
			}
		case got == nil || got.Mode != c.wantMode || (got.Pending == nil) != (c.wantPending == nil) || c.wantPending != nil && *got.Pending != *c.wantPending:
			t.Errorf("%s: %+v, want %s pending %v", c.name, got, c.wantMode, c.wantPending)
		}
	}
}
