package bootstrap_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aifake"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
)

// TestUses: 2e over HTTP with the whole platform: the read model of what an
// app uses, with its closed list of Attention, per tenant and permission.
func TestUses(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, do := aiStack(t, cluster, aifake.New())
	uses := func(token string) release.Uses {
		t.Helper()
		res := do(http.MethodGet, h.base+"/uses", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[release.Uses](t, res)
	}
	kinds := func(u release.Uses) []release.AttentionKind {
		out := []release.AttentionKind{}
		for _, a := range u.Attention {
			out = append(out, a.Kind)
		}
		return out
	}

	// a fresh online app: nothing to attend to
	u := uses(h.reader)
	if len(u.Attention) != 0 || u.Access.Mode != release.AccessPublic && u.Access.Mode != release.AccessTeam || u.Secrets.Total != 0 ||
		len(u.Resources) != 0 || u.AI.Enabled || u.AI.Status != ai.StateOff || !u.AI.Available {
		t.Fatalf("fresh app: %+v", u)
	}
	if u.Egress.Mode != "internet" || !u.Egress.Enforced {
		t.Fatalf("egress %+v", u.Egress)
	}

	// a requested secret nobody loaded: secret_missing with its name
	res := do(http.MethodPut, h.base+"/secrets/STRIPE_API_KEY", h.token, map[string]any{"source": "managed"})
	httpxtest.Want(t, res, http.StatusOK)
	u = uses(h.reader)
	if !slices.Equal(kinds(u), []release.AttentionKind{release.AttentionSecretMissing}) || !slices.Equal(u.Secrets.Missing, []string{"STRIPE_API_KEY"}) || u.Secrets.Total != 1 {
		t.Fatalf("missing secret: %+v", u)
	}

	// a plain variable counts, and is not a secret
	httpxtest.Want(t, do(http.MethodPatch, h.base+"/env", h.token, map[string]any{"set": map[string]string{"TZ": "America/Lima"}}), http.StatusAccepted)
	if u = uses(h.reader); u.Variables != 1 {
		t.Fatalf("variables %d", u.Variables)
	}

	// the cluster's policy is restricted and its agent has not answered
	cluster.SetEgress(release.ClusterEgress{Available: true, Mode: release.EgressRestricted, State: release.EgressPending})
	u = uses(h.reader)
	if !slices.Contains(kinds(u), release.AttentionEgressNotApplied) || u.Egress.Mode != release.EgressRestricted || u.Egress.Enforced {
		t.Fatalf("egress pending: %+v", u)
	}
	cluster.SetEgress(release.ClusterEgress{Available: true, Mode: release.EgressRestricted, State: release.EgressApplied, Enforced: true})
	if u = uses(h.reader); slices.Contains(kinds(u), release.AttentionEgressNotApplied) {
		t.Fatalf("egress applied still needs attention: %+v", u.Attention)
	}

	// the AI the app uses
	httpxtest.Want(t, do(http.MethodPut, h.base+"/ai", h.token, nil), http.StatusAccepted)
	eventually(t, "the AI is enabled", func() bool { return uses(h.reader).AI.Enabled })
	if u = uses(h.reader); u.AI.Status != ai.StateEnabled && u.AI.Status != ai.StateEnabling {
		t.Fatalf("ai %+v", u.AI)
	}

	// a cluster that stops answering takes its sections with it, not the card
	cluster.SetDown(errors.New("cluster unreachable"))
	res = do(http.MethodGet, h.base+"/uses", h.reader, nil)
	httpxtest.Want(t, res, http.StatusOK)
	u = httpxtest.JSON[release.Uses](t, res)
	if u.Secrets != nil || u.Egress != nil || !slices.Contains(u.Unavailable, release.SectionSecrets) || !slices.Contains(u.Unavailable, release.SectionEgress) ||
		u.Access.Mode == "" || u.Variables != 1 {
		t.Fatalf("cluster down: %+v", u)
	}
	cluster.SetDown(nil)

	// permission and tenant: projects:read only, and another organization's
	// environment is not found
	noPerm := httpxtest.Token(h.target.Organization, permission.AuditRead)
	httpxtest.Want(t, do(http.MethodGet, h.base+"/uses", noPerm, nil), http.StatusForbidden)
	httpxtest.Want(t, do(http.MethodGet, h.base+"/uses", h.stranger, nil), http.StatusNotFound)
	httpxtest.Want(t, do(http.MethodGet, h.base+"/uses", "", nil), http.StatusUnauthorized)
}
