package bootstrap_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aifake"
	"github.com/Abraxas-365/odyssey/internal/httpx/httpxtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/release/adapters/releasefake"
)

const itUser = "0d7c5c0e-3c1f-4f55-8a43-2f7f6d6a9a01"

// TestInventoryAndAttention: 3c over HTTP with the whole platform. The
// inventory is TI's (inventory:read) and per tenant; "Necesita tu atención"
// is each person's own apps and requests, plus what TI watches over for the
// holders of those permissions.
func TestInventoryAndAttention(t *testing.T) {
	cluster := releasefake.New(releasefake.Options{Behavior: func(release.DesiredApp) releasefake.Outcome { return releasefake.Outcome{} }})
	h, do := aiStack(t, cluster, aifake.New())
	org := h.target.Organization
	it := httpxtest.TokenAs(itUser, org, permission.ProjectsRead, permission.InventoryRead, permission.ApprovalsDecide)
	otherBuilder := httpxtest.TokenAs("5a1b8f0e-61a4-4b8e-9d3a-7c3c1d2e9b02", org, permission.ProjectsRead, permission.ProjectsWrite)
	stranger := httpxtest.TokenAs(itUser, identity.NewOrganizationID(), permission.ProjectsRead, permission.InventoryRead, permission.ApprovalsDecide)

	list := func(token, query string) inventory.Page {
		t.Helper()
		res := do(http.MethodGet, "/api/v1/inventory"+query, token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[inventory.Page](t, res)
	}
	attention := func(token string) inventory.Attention {
		t.Helper()
		res := do(http.MethodGet, "/api/v1/attention", token, nil)
		httpxtest.Want(t, res, http.StatusOK)
		return httpxtest.JSON[inventory.Attention](t, res)
	}
	kinds := func(a inventory.Attention) []inventory.Kind {
		out := []inventory.Kind{}
		for _, i := range a.Items {
			out = append(out, i.Kind)
		}
		return out
	}

	// only TI reads the inventory
	httpxtest.Want(t, do(http.MethodGet, "/api/v1/inventory", h.reader, nil), http.StatusForbidden)
	httpxtest.Want(t, do(http.MethodGet, "/api/v1/inventory", "", nil), http.StatusUnauthorized)

	// a fresh online app: one row, nothing to attend to
	page := list(it, "")
	if page.Page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("inventory %+v", page)
	}
	row := page.Items[0]
	if row.App.Name != "Pagos" || row.Owner.String() != httpxtest.User || row.App.Tier != "internal" || row.Status != inventory.StatusOnline ||
		row.Online == nil || row.Online.Image == "" || row.URL == "" || row.NeedsAttention || row.Access == "" {
		t.Fatalf("row %+v", row)
	}
	// an image published as it was is never scanned
	if row.Scan.State != inventory.ScanNone {
		t.Fatalf("scan %+v", row.Scan)
	}
	if len(row.Unavailable) != 0 {
		t.Fatalf("unavailable %v", row.Unavailable)
	}
	if a := attention(it); len(a.Items) != 0 || len(a.Unavailable) != 0 {
		t.Fatalf("an app in order still needs attention: %+v", a)
	}
	if a := attention(h.reader); len(a.Items) != 0 {
		t.Fatalf("attention of the owner %+v", a)
	}

	// a secret nobody loaded: the app needs its owner
	httpxtest.Want(t, do(http.MethodPut, h.base+"/secrets/STRIPE_API_KEY", h.token, map[string]any{"source": "managed"}), http.StatusOK)
	row = list(it, "?needs_attention=true").Items[0]
	if !row.NeedsAttention || row.Secrets.Total != 1 || len(row.Secrets.Missing) != 1 || row.Secrets.Missing[0] != "STRIPE_API_KEY" || len(row.Attention) == 0 {
		t.Fatalf("row with a missing secret: %+v", row)
	}
	mine := attention(h.reader) // the creator
	if len(mine.Items) != 1 || mine.Items[0].Kind != inventory.KindAppAttention || mine.Items[0].Scope != inventory.ScopeMine ||
		mine.Items[0].Detail != string(release.AttentionSecretMissing) || !strings.HasPrefix(mine.Items[0].Link, "/apps/") ||
		!strings.Contains(mine.Items[0].Sentence, "Pagos") || mine.Items[0].App == nil {
		t.Fatalf("owner's attention %+v", mine)
	}
	// somebody else's builder and TI do not get the owner's to-do
	if a := attention(otherBuilder); len(a.Items) != 0 {
		t.Fatalf("another builder sees %v", kinds(a))
	}
	if a := attention(it); len(a.Items) != 0 {
		t.Fatalf("TI sees the owner's to-do: %v", kinds(a))
	}

	// the filters
	if p := list(it, "?tier=production"); p.Page.Total != 0 || len(p.Items) != 0 {
		t.Fatalf("tier filter %+v", p)
	}
	if p := list(it, "?tier=internal&tier=sandbox&search=pag"); p.Page.Total != 1 {
		t.Fatalf("tier+search %+v", p)
	}
	if p := list(it, "?search=nothing-like-this"); p.Page.Total != 0 {
		t.Fatalf("search %+v", p)
	}
	if p := list(it, "?cluster=0d7c5c0e-3c1f-4f55-8a43-2f7f6d6a9a99"); p.Page.Total != 0 {
		t.Fatalf("cluster filter %+v", p)
	}
	if p := list(it, "?limit=1&offset=1"); p.Page.Total != 1 || len(p.Items) != 0 || p.Page.Offset != 1 {
		t.Fatalf("paging %+v", p)
	}
	for _, bad := range []string{"?tier=huge", "?needs_attention=maybe", "?format=xml", "?cluster=nope", "?limit=0"} {
		if res := do(http.MethodGet, "/api/v1/inventory"+bad, it, nil); res.Status != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", bad, res.Status)
		}
	}

	// the CSV: every row, no paging, spreadsheet-safe
	res := do(http.MethodGet, "/api/v1/inventory?format=csv", it, nil)
	httpxtest.Want(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") ||
		!strings.Contains(res.Header.Get("Content-Disposition"), "attachment") || res.Header.Get("X-Inventory-Truncated") != "false" {
		t.Fatalf("csv headers %v", res.Header)
	}
	body := string(res.Body)
	if !strings.Contains(body, "Pagos") || !strings.Contains(body, "STRIPE_API_KEY") || !strings.Contains(body, "Necesita atención") {
		t.Fatalf("csv:\n%s", body)
	}
	httpxtest.Want(t, do(http.MethodGet, "/api/v1/inventory?format=csv", h.reader, nil), http.StatusForbidden)

	// another organization sees nothing of it, even with the same permissions
	if p := list(stranger, ""); p.Page.Total != 0 || len(p.Items) != 0 {
		t.Fatalf("tenant leak in the inventory: %+v", p)
	}
	if a := attention(stranger); len(a.Items) != 0 {
		t.Fatalf("tenant leak in the attention: %+v", a)
	}
	if res := do(http.MethodGet, "/api/v1/inventory?format=csv", stranger, nil); strings.Contains(string(res.Body), "Pagos") {
		t.Fatal("tenant leak in the CSV")
	}
}
