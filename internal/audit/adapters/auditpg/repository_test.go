package auditpg_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/audit/adapters/auditpg"
	"github.com/Abraxas-365/odyssey/internal/audit/auditsvc"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type payload struct {
	N int `json:"n"`
}

var user = identity.MustParseUserID("00000000-0000-4000-8000-000000000002")

func ev(org identity.OrganizationID, typ string, n int) event.Event {
	e := event.New(typ, 1, org, user, "widget/1", "Cambió el widget", payload{N: n})
	event.Stamp(context.Background(), &e)
	return e
}

func setup(t *testing.T) (*sqlx.DB, *auditsvc.Service) {
	db := pgtest.DB(t, migrate.Set{Owner: auditpg.Schema, Files: auditpg.Migrations()})
	return db, auditsvc.New(auditpg.New(db), txn.Postgres{DB: db})
}

func TestChainIsIdempotentOrderedAndPerOrganization(t *testing.T) {
	_, svc := setup(t)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	first := ev(org, "widget.created", 1)
	for _, e := range []event.Event{first, first, ev(org, "widget.changed", 2), ev(other, "widget.created", 1)} {
		if err := svc.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, org, audit.Filter{}, query.Pagination{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Page.Total != 2 || page.Items[0].Seq != 2 || page.Items[1].Seq != 1 || page.Items[0].PrevHash != page.Items[1].Hash {
		t.Fatalf("chain %+v", page)
	}
	if v, _ := svc.Verify(ctx, other); !v.Valid || v.Entries != 1 {
		t.Fatalf("other organization has its own chain: %+v", v)
	}
	page, _ = svc.List(ctx, org, audit.Filter{Types: []string{"widget.changed"}, Actor: user}, query.Pagination{})
	if page.Page.Total != 1 {
		t.Fatalf("filter %+v", page.Page)
	}
	page, _ = svc.List(ctx, org, audit.Filter{CorrelationID: first.CorrelationID}, query.Pagination{Search: "widget"})
	if page.Page.Total != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("correlation filter %+v", page.Page)
	}
}

func TestConcurrentRecordsKeepTheChainLinear(t *testing.T) {
	_, svc := setup(t)
	ctx := context.Background()
	org := identity.NewOrganizationID()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := svc.Record(ctx, ev(org, "widget.changed", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if v, err := svc.Verify(ctx, org); err != nil || !v.Valid || v.Entries != 20 {
		t.Fatalf("verify %+v %v", v, err)
	}
}

func TestTamperingIsRefusedOrDetected(t *testing.T) {
	db, svc := setup(t)
	ctx := context.Background()
	org := identity.NewOrganizationID()
	for i := range 3 {
		if err := svc.Record(ctx, ev(org, "widget.changed", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE audit.entries SET summary = 'nada' WHERE seq = 2`); err == nil {
		t.Fatal("updates must be rejected")
	}
	if _, err := db.Exec(`DELETE FROM audit.entries WHERE seq = 2`); err == nil {
		t.Fatal("deletes must be rejected")
	}
	// someone with DDL rights disables the guard and edits history
	if _, err := db.Exec(`ALTER TABLE audit.entries DISABLE TRIGGER entries_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE audit.entries SET summary = 'nada' WHERE organization_id = $1 AND seq = 2`, org); err != nil {
		t.Fatal(err)
	}
	if v, err := svc.Verify(ctx, org); err != nil || v.Valid || v.BrokenAt == nil || *v.BrokenAt != 2 {
		t.Fatalf("verify after tampering %+v %v", v, err)
	}

	// cutting off the newest entries leaves a valid-looking prefix: the head catches it
	other := identity.NewOrganizationID()
	for i := range 3 {
		if err := svc.Record(ctx, ev(other, "widget.changed", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM audit.entries WHERE organization_id = $1 AND seq = 3`, other); err != nil {
		t.Fatal(err)
	}
	if v, err := svc.Verify(ctx, other); err != nil || v.Valid || v.BrokenAt == nil || *v.BrokenAt != 3 {
		t.Fatalf("verify after cutting the tail %+v %v", v, err)
	}
	if _, err := db.Exec(`ALTER TABLE audit.entries ENABLE TRIGGER entries_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`TRUNCATE audit.entries`); err == nil {
		t.Fatal("truncate must be rejected")
	}
}
